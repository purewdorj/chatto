package push

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"

	"github.com/chattocorp/chatto/cli/internal/config"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

const (
	fcmBaseURL         = "https://fcm.googleapis.com"
	fcmDefaultTokenURL = "https://oauth2.googleapis.com/token"
	fcmScope           = "https://www.googleapis.com/auth/firebase.messaging"
	// fcmMessagesChannel is the Android notification channel ID that the
	// Chatto Android app creates for message notifications. Call pushes are
	// data-only, so the app chooses their channel itself.
	fcmMessagesChannel = "messages"
	// fcmErrorTypeURL identifies FCM error details in a Google API error.
	fcmErrorTypeURL = "type.googleapis.com/google.firebase.fcm.v1.FcmError"
	// badRequestTypeURL identifies field violations in a Google API error.
	badRequestTypeURL = "type.googleapis.com/google.rpc.BadRequest"
)

// fcmServiceAccount is the part of a Google service account key that FCM
// authentication needs.
type fcmServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// fcmClient sends notification messages and data-only call messages through
// the FCM HTTP v1 API.
type fcmClient struct {
	sendURL         string
	tokens          oauth2.TokenSource
	includePreviews bool
	httpClient      *http.Client
}

func newFCMClient(cfg config.FCMConfig, httpClient *http.Client, opts NativeSenderOptions) (*fcmClient, error) {
	raw, err := cfg.ServiceAccountKey()
	if err != nil {
		return nil, err
	}
	var account fcmServiceAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		return nil, errors.New("push.fcm service account key is not valid JSON")
	}
	if account.Type != "service_account" || account.ClientEmail == "" || account.PrivateKey == "" {
		return nil, errors.New("push.fcm service account key must be a service_account key with client_email and private_key")
	}
	projectID := strings.TrimSpace(cfg.ProjectID)
	if projectID == "" {
		projectID = account.ProjectID
	}
	if projectID == "" {
		return nil, errors.New("push.fcm.project_id is required when the service account key has no project_id")
	}
	tokenURL := account.TokenURI
	if tokenURL == "" {
		tokenURL = fcmDefaultTokenURL
	}
	if opts.FCMTokenURL != "" {
		tokenURL = opts.FCMTokenURL
	} else if parsed, err := url.Parse(tokenURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("push.fcm service account token_uri must be an HTTPS URL")
	}
	base := fcmBaseURL
	if opts.FCMBaseURL != "" {
		base = strings.TrimRight(opts.FCMBaseURL, "/")
	}

	jwtConfig := &jwt.Config{
		Email:        account.ClientEmail,
		PrivateKey:   []byte(account.PrivateKey),
		PrivateKeyID: account.PrivateKeyID,
		Scopes:       []string{fcmScope},
		TokenURL:     tokenURL,
	}
	// The token source keeps this context for every token refresh. It carries
	// only the HTTP client; each refresh is bounded by the client timeout.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)
	tokens := oauth2.ReuseTokenSource(nil, jwtConfig.TokenSource(tokenCtx))
	if err := validateFCMPrivateKey(jwtConfig); err != nil {
		return nil, err
	}
	return &fcmClient{
		sendURL:         base + "/v1/projects/" + url.PathEscape(projectID) + "/messages:send",
		tokens:          tokens,
		includePreviews: cfg.IncludePreviewsOrDefault(),
		httpClient:      httpClient,
	}, nil
}

// validateFCMPrivateKey parses the service account key without a network
// request, so an unusable key fails startup. The oauth2 package parses the
// key only when it signs the first token request.
func validateFCMPrivateKey(cfg *jwt.Config) error {
	block, _ := pem.Decode(cfg.PrivateKey)
	if block == nil {
		return errors.New("push.fcm service account private_key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if _, ok := key.(*rsa.PrivateKey); ok {
			return nil
		}
		return errors.New("push.fcm service account private_key is not an RSA key")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}
	return errors.New("push.fcm service account private_key is not a usable RSA key")
}

type fcmNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type fcmAndroidNotification struct {
	ChannelID string `json:"channel_id"`
	Sound     string `json:"sound"`
	Tag       string `json:"tag,omitempty"`
}

type fcmAndroidConfig struct {
	Priority              string                  `json:"priority"`
	TTL                   string                  `json:"ttl"`
	RestrictedPackageName string                  `json:"restricted_package_name,omitempty"`
	Notification          *fcmAndroidNotification `json:"notification,omitempty"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification *fcmNotification  `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
	Android      fcmAndroidConfig  `json:"android"`
}

// fcmBody builds the JSON body of an FCM v1 send request. A call or call-end
// push is a data-only message: it has no notification block, so Android
// always delivers it to the messaging service of the app, which shows a
// full-screen call notification or stops the ring. Other kinds are
// notification messages on the messages channel.
func (c *fcmClient) fcmBody(device *runtimestatev1.NativePushDevice, payload *Payload, ttl time.Duration) ([]byte, error) {
	seconds := max(int64(ttl/time.Second), 0)
	message := fcmMessage{
		Token: device.GetToken(),
		Data:  nativeData(payload, c.includePreviews),
		Android: fcmAndroidConfig{
			Priority:              "HIGH",
			TTL:                   fmt.Sprintf("%ds", seconds),
			RestrictedPackageName: device.GetAppId(),
		},
	}
	if !payload.kindOrDefault().IsCallKind() {
		title, body := nativeContent(payload, c.includePreviews)
		message.Notification = &fcmNotification{Title: title, Body: body}
		message.Android.Notification = &fcmAndroidNotification{
			ChannelID: fcmMessagesChannel,
			Sound:     "default",
			Tag:       payload.Tag,
		}
	}
	return json.Marshal(map[string]any{"message": message})
}

func (c *fcmClient) send(ctx context.Context, device *runtimestatev1.NativePushDevice, payload *Payload, ttl time.Duration) nativeOutcome {
	body, err := c.fcmBody(device, payload, ttl)
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: encode FCM payload")}
	}
	token, err := c.tokens.Token()
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: FCM authentication failed")}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sendURL, bytes.NewReader(body))
	if err != nil {
		return nativeOutcome{err: errors.New("native push delivery failed: build FCM request")}
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nativeOutcome{err: nativeRequestError(err)}
	}
	defer response.Body.Close()
	responseBody := readNativeResponseBody(response.Body)
	if response.StatusCode == http.StatusOK {
		return nativeOutcome{success: true}
	}
	code := fcmErrorCode(responseBody)
	if fcmTokenGone(code, responseBody) {
		return nativeOutcome{gone: true, err: nativeStatusError("FCM", response.StatusCode, code)}
	}
	return nativeOutcome{err: nativeStatusError("FCM", response.StatusCode, code)}
}

// fcmErrorResponse is the Google API error body that FCM returns.
type fcmErrorResponse struct {
	Error struct {
		Status  string `json:"status"`
		Details []struct {
			Type            string `json:"@type"`
			ErrorCode       string `json:"errorCode"`
			FieldViolations []struct {
				Field string `json:"field"`
			} `json:"fieldViolations"`
		} `json:"details"`
	} `json:"error"`
}

// fcmErrorCode returns the FCM error code, or the Google API status when the
// body has no FCM error detail. It returns only known identifier values.
func fcmErrorCode(body []byte) string {
	var parsed fcmErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, detail := range parsed.Error.Details {
		if detail.Type == fcmErrorTypeURL && detail.ErrorCode != "" {
			return providerReasonLabel(detail.ErrorCode)
		}
	}
	return providerReasonLabel(parsed.Error.Status)
}

// fcmTokenGone reports whether an FCM error proves that the registration
// token is permanently invalid. UNREGISTERED and SENDER_ID_MISMATCH always
// refer to the token. INVALID_ARGUMENT can also mean a bad message, so it
// counts only when Google names message.token as the invalid field. This keeps
// a payload fault from removing every device.
func fcmTokenGone(code string, body []byte) bool {
	switch code {
	case "UNREGISTERED", "SENDER_ID_MISMATCH":
		return true
	case "INVALID_ARGUMENT":
	default:
		return false
	}
	var parsed fcmErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	for _, detail := range parsed.Error.Details {
		if detail.Type != badRequestTypeURL {
			continue
		}
		for _, violation := range detail.FieldViolations {
			if violation.Field == "message.token" {
				return true
			}
		}
	}
	return false
}
