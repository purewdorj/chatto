package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/chattocorp/chatto/cli/internal/config"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

const (
	apnsProductionURL = "https://api.push.apple.com"
	apnsSandboxURL    = "https://api.sandbox.push.apple.com"
	// apnsTokenRefresh is the age at which the sender signs a new provider
	// token. Apple rejects tokens older than one hour and throttles tokens
	// that change more often than every 20 minutes.
	apnsTokenRefresh = 40 * time.Minute
	// apnsMaxCollapseIDBytes is the APNs limit for apns-collapse-id.
	apnsMaxCollapseIDBytes = 64
	// apnsMaxPayloadBytes is the APNs limit for an alert payload.
	apnsMaxPayloadBytes = 4096
	// apnsMaxVoIPPayloadBytes is the APNs limit for a VoIP payload.
	apnsMaxVoIPPayloadBytes = 5120
	// apnsVoIPTopicSuffix follows the bundle ID in the APNs topic of a VoIP
	// push.
	apnsVoIPTopicSuffix = ".voip"
)

// apnsGoneReasons are the APNs error reasons that mean that the device token
// can never receive pushes for this topic and environment again.
var apnsGoneReasons = map[string]bool{
	"BadDeviceToken":         true,
	"Unregistered":           true,
	"DeviceTokenNotForTopic": true,
}

// apnsClient sends alert pushes to APNs with token-based authentication.
type apnsClient struct {
	teamID          string
	keyID           string
	key             *ecdsa.PrivateKey
	includePreviews bool
	httpClient      *http.Client
	productionURL   string
	sandboxURL      string
	now             func() time.Time

	mu          sync.Mutex
	token       string
	tokenIssued time.Time
}

func newAPNsClient(cfg config.APNsConfig, httpClient *http.Client, opts NativeSenderOptions, now func() time.Time) (*apnsClient, error) {
	pemBytes, err := cfg.PrivateKeyPEM()
	if err != nil {
		return nil, err
	}
	key, err := parseAPNsKey(pemBytes)
	if err != nil {
		return nil, err
	}
	client := &apnsClient{
		teamID:          cfg.TeamID,
		keyID:           cfg.KeyID,
		key:             key,
		includePreviews: cfg.IncludePreviewsOrDefault(),
		httpClient:      httpClient,
		productionURL:   apnsProductionURL,
		sandboxURL:      apnsSandboxURL,
		now:             now,
	}
	if opts.APNsProductionURL != "" {
		client.productionURL = strings.TrimRight(opts.APNsProductionURL, "/")
	}
	if opts.APNsSandboxURL != "" {
		client.sandboxURL = strings.TrimRight(opts.APNsSandboxURL, "/")
	}
	// Sign once at startup so an unusable key fails startup.
	if _, err := client.providerToken(); err != nil {
		return nil, err
	}
	return client, nil
}

// parseAPNsKey parses the PKCS #8 P-256 key of an APNs .p8 file.
func parseAPNsKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("push.apns private key is not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("push.apns private key is not a PKCS #8 key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return nil, errors.New("push.apns private key is not a P-256 ECDSA key")
	}
	return key, nil
}

// providerToken returns the cached ES256 provider token, and signs a new one
// when the cached token reaches apnsTokenRefresh.
func (c *apnsClient) providerToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Sub(c.tokenIssued) < apnsTokenRefresh && !now.Before(c.tokenIssued) {
		return c.token, nil
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": c.teamID,
		"iat": now.Unix(),
	})
	token.Header["kid"] = c.keyID
	signed, err := token.SignedString(c.key)
	if err != nil {
		return "", errors.New("sign APNs provider token")
	}
	c.token = signed
	c.tokenIssued = now
	return signed, nil
}

// invalidateProviderToken drops the cached token after APNs rejected it.
func (c *apnsClient) invalidateProviderToken(rejected string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == rejected {
		c.token = ""
	}
}

type apnsAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type apnsAPS struct {
	Alert             apnsAlert `json:"alert"`
	Sound             string    `json:"sound,omitempty"`
	ThreadID          string    `json:"thread-id,omitempty"`
	InterruptionLevel string    `json:"interruption-level,omitempty"`
}

// apnsBody builds the JSON body of an APNs alert push. The custom data keys
// sit beside the aps dictionary, where the iOS app reads them.
func (c *apnsClient) apnsBody(payload *Payload) ([]byte, error) {
	title, body := nativeContent(payload, c.includePreviews)
	aps := apnsAPS{
		Alert:             apnsAlert{Title: title, Body: body},
		Sound:             "default",
		ThreadID:          payload.GroupID,
		InterruptionLevel: "active",
	}
	if payload.kindOrDefault() == KindCall {
		aps.InterruptionLevel = "time-sensitive"
	}
	document := map[string]any{"aps": aps}
	for key, value := range nativeData(payload, c.includePreviews) {
		document[key] = value
	}
	return json.Marshal(document)
}

// apnsVoIPBody builds the JSON body of an APNs VoIP push. The aps dictionary
// is empty, because PushKit shows nothing by itself: the app reads the custom
// data keys and reports the call to CallKit.
func (c *apnsClient) apnsVoIPBody(payload *Payload) ([]byte, error) {
	document := map[string]any{"aps": map[string]any{}}
	for key, value := range nativeData(payload, c.includePreviews) {
		document[key] = value
	}
	return json.Marshal(document)
}

// apnsRequest describes one APNs request after the push-type choice.
type apnsRequest struct {
	body      []byte
	topic     string
	pushType  string
	collapse  string
	expiresAt time.Time
}

// send delivers an alert push to an APNs device token.
func (c *apnsClient) send(ctx context.Context, device *runtimestatev1.NativePushDevice, payload *Payload, expiresAt time.Time) nativeOutcome {
	body, err := c.apnsBody(payload)
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: encode APNs payload")}
	}
	if len(body) > apnsMaxPayloadBytes {
		return nativeOutcome{err: errors.New("native push delivery failed: payload too large")}
	}
	collapse := ""
	if tag := payload.Tag; tag != "" && len(tag) <= apnsMaxCollapseIDBytes {
		collapse = tag
	}
	return c.do(ctx, device, apnsRequest{body: body, topic: device.GetAppId(), pushType: "alert", collapse: collapse, expiresAt: expiresAt})
}

// sendVoIP delivers a VoIP push to a PushKit device token. The topic is the
// bundle ID with the ".voip" suffix. The push has no collapse ID, so a
// call-end push never replaces the call push that it stops.
func (c *apnsClient) sendVoIP(ctx context.Context, device *runtimestatev1.NativePushDevice, payload *Payload, expiresAt time.Time) nativeOutcome {
	body, err := c.apnsVoIPBody(payload)
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: encode APNs VoIP payload")}
	}
	if len(body) > apnsMaxVoIPPayloadBytes {
		return nativeOutcome{err: errors.New("native push delivery failed: payload too large")}
	}
	return c.do(ctx, device, apnsRequest{body: body, topic: device.GetAppId() + apnsVoIPTopicSuffix, pushType: "voip", expiresAt: expiresAt})
}

// do sends one APNs request and classifies the response.
func (c *apnsClient) do(ctx context.Context, device *runtimestatev1.NativePushDevice, req apnsRequest) nativeOutcome {
	body := req.body
	providerToken, err := c.providerToken()
	if err != nil {
		return nativeOutcome{err: err}
	}
	base := c.productionURL
	if device.GetApnsSandbox() {
		base = c.sandboxURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+url.PathEscape(device.GetToken()), bytes.NewReader(body))
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: build APNs request")}
	}
	request.Header.Set("authorization", "bearer "+providerToken)
	request.Header.Set("content-type", "application/json")
	request.Header.Set("apns-topic", req.topic)
	request.Header.Set("apns-push-type", req.pushType)
	request.Header.Set("apns-priority", "10")
	request.Header.Set("apns-expiration", strconv.FormatInt(req.expiresAt.Unix(), 10))
	if req.collapse != "" {
		request.Header.Set("apns-collapse-id", req.collapse)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nativeOutcome{err: nativeRequestError(err)}
	}
	defer response.Body.Close()
	responseBody := readNativeResponseBody(response.Body)
	if response.StatusCode == http.StatusOK {
		return nativeOutcome{success: true}
	}
	var apnsError struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(responseBody, &apnsError)
	reason := providerReasonLabel(apnsError.Reason)
	if response.StatusCode == http.StatusGone || apnsGoneReasons[apnsError.Reason] {
		return nativeOutcome{gone: true, err: nativeStatusError("APNs", response.StatusCode, reason)}
	}
	if apnsError.Reason == "ExpiredProviderToken" || apnsError.Reason == "InvalidProviderToken" {
		c.invalidateProviderToken(providerToken)
	}
	return nativeOutcome{err: nativeStatusError("APNs", response.StatusCode, reason)}
}
