package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/golang-jwt/jwt/v5"

	"github.com/chattocorp/chatto/cli/internal/config"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

const (
	testAPNsToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testFCMToken  = "fcm-registration:token_123"
	testAppID     = "mn.setunari.chatto.mobile"
)

// recordedRequest is one request that a fake provider received.
type recordedRequest struct {
	path   string
	header http.Header
	body   []byte
}

// fakeProvider is an HTTPS test server that records requests and answers with
// a configurable status and body.
type fakeProvider struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	status   int
	body     string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	provider := &fakeProvider{status: http.StatusOK}
	provider.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		provider.mu.Lock()
		provider.requests = append(provider.requests, recordedRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		status, responseBody := provider.status, provider.body
		provider.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, responseBody)
	}))
	provider.server.EnableHTTP2 = true
	provider.server.StartTLS()
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *fakeProvider) respond(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.body = status, body
}

func (p *fakeProvider) last(t *testing.T) recordedRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		t.Fatal("provider received no request")
	}
	return p.requests[len(p.requests)-1]
}

func (p *fakeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func testAPNsKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func testServiceAccount(t *testing.T, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	account, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "chatto-test",
		"private_key_id": "test-key-id",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "push@chatto-test.iam.gserviceaccount.com",
		"token_uri":      tokenURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(account)
}

// nativeTestEnv is a native sender wired to fake APNs, FCM, and OAuth servers.
type nativeTestEnv struct {
	sender      *NativeSender
	apnsKey     *ecdsa.PrivateKey
	production  *fakeProvider
	sandbox     *fakeProvider
	fcm         *fakeProvider
	oauth       *fakeProvider
	clock       *testClock
	pushConfig  config.PushConfig
	httpClient  *http.Client
	senderOpts  NativeSenderOptions
	apnsKeyPEM  string
	accountJSON string
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newNativeTestEnv(t *testing.T, mutate func(*config.PushConfig)) *nativeTestEnv {
	t.Helper()
	env := &nativeTestEnv{
		production: newFakeProvider(t),
		sandbox:    newFakeProvider(t),
		fcm:        newFakeProvider(t),
		oauth:      newFakeProvider(t),
		clock:      &testClock{now: time.Now()},
	}
	env.oauth.respond(http.StatusOK, `{"access_token":"fcm-access-token","token_type":"Bearer","expires_in":3600}`)
	env.apnsKey, env.apnsKeyPEM = testAPNsKey(t)
	env.accountJSON = testServiceAccount(t, "https://oauth.invalid/token")
	env.pushConfig = config.PushConfig{
		APNs: config.APNsConfig{
			Enabled:    true,
			TeamID:     "TEAM123456",
			KeyID:      "KEY1234567",
			PrivateKey: env.apnsKeyPEM,
			Topics:     []string{testAppID},
		},
		FCM: config.FCMConfig{
			Enabled:            true,
			ServiceAccountJSON: env.accountJSON,
			AppIDs:             []string{testAppID},
		},
	}
	if mutate != nil {
		mutate(&env.pushConfig)
	}
	roots := x509.NewCertPool()
	for _, provider := range []*fakeProvider{env.production, env.sandbox, env.fcm, env.oauth} {
		roots.AddCert(provider.server.Certificate())
	}
	env.httpClient = &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots},
		ForceAttemptHTTP2: true,
	}}
	env.senderOpts = NativeSenderOptions{
		HTTPClient:        env.httpClient,
		APNsProductionURL: env.production.server.URL,
		APNsSandboxURL:    env.sandbox.server.URL,
		FCMBaseURL:        env.fcm.server.URL,
		FCMTokenURL:       env.oauth.server.URL + "/token",
		Now:               env.clock.Now,
	}
	sender, err := NewNativeSenderWithOptions(env.pushConfig, log.New(io.Discard), env.senderOpts)
	if err != nil {
		t.Fatalf("NewNativeSenderWithOptions: %v", err)
	}
	env.sender = sender
	return env
}

func apnsDevice(sandbox bool) *runtimestatev1.NativePushDevice {
	return &runtimestatev1.NativePushDevice{
		Platform:    runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		Token:       testAPNsToken,
		AppId:       testAppID,
		ApnsSandbox: sandbox,
	}
}

func fcmDevice() *runtimestatev1.NativePushDevice {
	return &runtimestatev1.NativePushDevice{
		Platform: runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM,
		Token:    testFCMToken,
		AppId:    testAppID,
	}
}

func messagePayload(deadline time.Time) *Payload {
	return &Payload{
		Title:          "@alice mentioned you in #general",
		Body:           "secret preview text",
		URL:            "https://chat.example.test/chat/-/room-1/thread-1?highlight=event-1",
		Tag:            "mention-event-1",
		NotificationID: "occurrence-1",
		ServerOrigin:   "https://chat.example.test",
		RecipientID:    "user-1",
		AttentionLevel: "important",
		GroupID:        "room-1",
		Kind:           KindMessage,
		// Web Push fields that native push must ignore.
		Icon:             "https://chat.example.test/icons/icon-192.png",
		DeliveryDeadline: deadline,
	}
}

func TestNativeSenderNotConfigured(t *testing.T) {
	sender, err := NewNativeSender(config.PushConfig{Enabled: true}, nil)
	if err != nil || sender != nil {
		t.Fatalf("NewNativeSender without native config = (%v, %v), want (nil, nil)", sender, err)
	}
	if sender.Supports(runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS) {
		t.Fatal("nil sender supports APNs")
	}
}

func TestNativeSenderRejectsUnusableKeys(t *testing.T) {
	_, ecPEM := testAPNsKey(t)
	tests := []struct {
		name   string
		config config.PushConfig
	}{
		{"APNs key is not PEM", config.PushConfig{APNs: config.APNsConfig{Enabled: true, TeamID: "TEAM123456", KeyID: "KEY1234567", PrivateKey: "not a key", Topics: []string{testAppID}}}},
		{"APNs key file missing", config.PushConfig{APNs: config.APNsConfig{Enabled: true, TeamID: "TEAM123456", KeyID: "KEY1234567", PrivateKeyFile: t.TempDir() + "/missing.p8", Topics: []string{testAppID}}}},
		{"FCM JSON invalid", config.PushConfig{FCM: config.FCMConfig{Enabled: true, ServiceAccountJSON: "{", AppIDs: []string{testAppID}}}},
		{"FCM key is not RSA", config.PushConfig{FCM: config.FCMConfig{Enabled: true, AppIDs: []string{testAppID}, ServiceAccountJSON: `{"type":"service_account","project_id":"p","client_email":"a@b","private_key":` + strconv.Quote(ecPEM) + `}`}}},
		{"FCM token URI is not HTTPS", config.PushConfig{FCM: config.FCMConfig{Enabled: true, AppIDs: []string{testAppID}, ServiceAccountJSON: testServiceAccount(t, "http://oauth.example.test/token")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender, err := NewNativeSender(tt.config, log.New(io.Discard))
			if err == nil || sender != nil {
				t.Fatalf("NewNativeSender = (%v, %v), want an error", sender, err)
			}
		})
	}
}

func TestAPNsMessagePayloadAndHeaders(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	deadline := env.clock.Now().Add(90 * time.Second)

	result := env.sender.Send(context.Background(), apnsDevice(false), messagePayload(deadline))
	if !result.Success || result.Gone || result.Error != nil {
		t.Fatalf("Send = %+v, want success", result)
	}
	if env.sandbox.count() != 0 {
		t.Fatal("production device used the sandbox host")
	}
	request := env.production.last(t)
	if request.path != "/3/device/"+testAPNsToken {
		t.Fatalf("path = %q", request.path)
	}
	wantHeaders := map[string]string{
		"apns-topic":       testAppID,
		"apns-push-type":   "alert",
		"apns-priority":    "10",
		"apns-expiration":  strconv.FormatInt(deadline.Unix(), 10),
		"apns-collapse-id": "mention-event-1",
	}
	for name, want := range wantHeaders {
		if got := request.header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	bearer := strings.TrimPrefix(request.header.Get("authorization"), "bearer ")
	parsed, err := jwt.Parse(bearer, func(token *jwt.Token) (any, error) {
		return &env.apnsKey.PublicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("provider token does not verify: %v", err)
	}
	if parsed.Header["kid"] != "KEY1234567" {
		t.Fatalf("kid = %v", parsed.Header["kid"])
	}
	if issuer, _ := parsed.Claims.GetIssuer(); issuer != "TEAM123456" {
		t.Fatalf("iss = %q", issuer)
	}

	var body map[string]any
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatal(err)
	}
	aps := body["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "@alice mentioned you in #general" || alert["body"] != "secret preview text" {
		t.Fatalf("alert = %v", alert)
	}
	if aps["sound"] != "default" || aps["thread-id"] != "room-1" || aps["interruption-level"] != "active" {
		t.Fatalf("aps = %v", aps)
	}
	for key, want := range map[string]string{
		"kind":           "message",
		"serverOrigin":   "https://chat.example.test",
		"url":            "https://chat.example.test/chat/-/room-1/thread-1?highlight=event-1",
		"notificationId": "occurrence-1",
		"attentionLevel": "important",
		"recipientId":    "user-1",
	} {
		if body[key] != want {
			t.Errorf("data %s = %v, want %q", key, body[key], want)
		}
	}
	if _, ok := body["icon"]; ok {
		t.Error("APNs payload contains the Web Push icon")
	}
	if _, ok := aps["badge"]; ok {
		t.Error("APNs payload sets a badge")
	}
}

func TestAPNsSandboxCallAlert(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	payload := BuildIncomingCallPayload("https://chat.example.test/", "room-1", "call-1", "user-2", "Alice")

	result := env.sender.Send(context.Background(), apnsDevice(true), payload)
	if !result.Success {
		t.Fatalf("Send = %+v, want success", result)
	}
	if env.production.count() != 0 {
		t.Fatal("sandbox device used the production host")
	}
	request := env.sandbox.last(t)
	expiration, err := strconv.ParseInt(request.header.Get("apns-expiration"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if max := env.clock.Now().Add(callAlertTTL).Unix(); expiration > max || expiration <= env.clock.Now().Unix() {
		t.Fatalf("call expiration = %d, want at most %d", expiration, max)
	}
	if request.header.Get("apns-priority") != "10" {
		t.Fatalf("call priority = %q", request.header.Get("apns-priority"))
	}
	var body map[string]any
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatal(err)
	}
	aps := body["aps"].(map[string]any)
	if aps["interruption-level"] != "time-sensitive" || aps["sound"] != "default" || aps["thread-id"] != "room-1" {
		t.Fatalf("call aps = %v", aps)
	}
	if body["kind"] != "call" || body["callId"] != "call-1" || body["url"] != "https://chat.example.test/chat/-/room-1" {
		t.Fatalf("call data = %v", body)
	}
}

func TestNativePreviewsCanBeOmitted(t *testing.T) {
	disabled := false
	env := newNativeTestEnv(t, func(cfg *config.PushConfig) {
		cfg.APNs.IncludePreviews = &disabled
		cfg.FCM.IncludePreviews = &disabled
	})
	deadline := env.clock.Now().Add(time.Minute)

	env.sender.Send(context.Background(), apnsDevice(false), messagePayload(deadline))
	apnsBody := string(env.production.last(t).body)
	env.sender.Send(context.Background(), fcmDevice(), messagePayload(deadline))
	fcmBody := string(env.fcm.last(t).body)
	for name, body := range map[string]string{"APNs": apnsBody, "FCM": fcmBody} {
		if strings.Contains(body, "secret preview text") || strings.Contains(body, "alice") || strings.Contains(body, "general") {
			t.Errorf("%s payload contains private content: %s", name, body)
		}
		if !strings.Contains(body, nativeGenericMessageTitle) {
			t.Errorf("%s payload has no generic title: %s", name, body)
		}
	}

	env.sender.Send(context.Background(), apnsDevice(false), BuildIncomingCallPayload("https://chat.example.test", "room-1", "call-1", "user-2", "Alice"))
	if body := string(env.production.last(t).body); strings.Contains(body, "Alice") || !strings.Contains(body, nativeGenericCallTitle) {
		t.Errorf("call payload without previews = %s", body)
	}

	env.sender.Send(context.Background(), apnsDevice(false), BuildNativeTestPayload("https://chat.example.test", "user-1"))
	if body := string(env.production.last(t).body); !strings.Contains(body, "Test notification") {
		t.Errorf("test payload lost its text: %s", body)
	}
}

func TestAPNsResponseClassification(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantGone bool
	}{
		{"410 unregistered", http.StatusGone, `{"reason":"Unregistered"}`, true},
		{"bad device token", http.StatusBadRequest, `{"reason":"BadDeviceToken"}`, true},
		{"token not for topic", http.StatusBadRequest, `{"reason":"DeviceTokenNotForTopic"}`, true},
		{"bad topic", http.StatusBadRequest, `{"reason":"BadTopic"}`, false},
		{"payload too large", http.StatusRequestEntityTooLarge, `{"reason":"PayloadTooLarge"}`, false},
		{"server error", http.StatusInternalServerError, `{"reason":"InternalServerError"}`, false},
		{"unreadable body", http.StatusServiceUnavailable, `<html>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newNativeTestEnv(t, nil)
			env.production.respond(tt.status, tt.body)
			result := env.sender.Send(context.Background(), apnsDevice(false), messagePayload(time.Time{}))
			if result.Success || result.Gone != tt.wantGone || result.Error == nil {
				t.Fatalf("Send = %+v, want gone=%v with an error", result, tt.wantGone)
			}
			if strings.Contains(result.Error.Error(), "<html>") {
				t.Fatalf("error exposes provider body: %v", result.Error)
			}
		})
	}
}

func TestAPNsProviderTokenRefresh(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	send := func() string {
		t.Helper()
		env.sender.Send(context.Background(), apnsDevice(false), messagePayload(time.Time{}))
		return env.production.last(t).header.Get("authorization")
	}
	first := send()
	env.clock.Advance(time.Second)
	if second := send(); second != first {
		t.Fatal("provider token changed before the refresh age")
	}
	env.clock.Advance(apnsTokenRefresh)
	refreshed := send()
	if refreshed == first {
		t.Fatal("provider token was not refreshed after the refresh age")
	}

	env.production.respond(http.StatusForbidden, `{"reason":"ExpiredProviderToken"}`)
	env.clock.Advance(time.Second)
	send()
	env.production.respond(http.StatusOK, "")
	if afterReject := send(); afterReject == refreshed {
		t.Fatal("provider token was reused after APNs rejected it")
	}
}

func TestFCMMessageAndCallPayloads(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	deadline := env.clock.Now().Add(100 * time.Second)

	result := env.sender.Send(context.Background(), fcmDevice(), messagePayload(deadline))
	if !result.Success {
		t.Fatalf("Send = %+v, want success", result)
	}
	tokenRequest := env.oauth.last(t)
	form, err := url.ParseQuery(string(tokenRequest.body))
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || form.Get("assertion") == "" {
		t.Fatalf("token request = %v", form)
	}

	request := env.fcm.last(t)
	if request.path != "/v1/projects/chatto-test/messages:send" {
		t.Fatalf("FCM path = %q", request.path)
	}
	if request.header.Get("Authorization") != "Bearer fcm-access-token" {
		t.Fatalf("FCM authorization = %q", request.header.Get("Authorization"))
	}
	var body struct {
		Message struct {
			Token        string            `json:"token"`
			Notification map[string]string `json:"notification"`
			Data         map[string]string `json:"data"`
			Android      struct {
				Priority              string            `json:"priority"`
				TTL                   string            `json:"ttl"`
				RestrictedPackageName string            `json:"restricted_package_name"`
				Notification          map[string]string `json:"notification"`
			} `json:"android"`
		} `json:"message"`
	}
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatal(err)
	}
	message := body.Message
	if message.Token != testFCMToken || message.Notification["title"] != "@alice mentioned you in #general" || message.Notification["body"] != "secret preview text" {
		t.Fatalf("message = %+v", message)
	}
	if message.Android.Priority != "HIGH" || message.Android.TTL != "100s" || message.Android.RestrictedPackageName != testAppID {
		t.Fatalf("android = %+v", message.Android)
	}
	if message.Android.Notification["channel_id"] != fcmMessagesChannel || message.Android.Notification["sound"] != "default" || message.Android.Notification["tag"] != "mention-event-1" {
		t.Fatalf("android notification = %v", message.Android.Notification)
	}
	if message.Data["kind"] != "message" || message.Data["notificationId"] != "occurrence-1" || message.Data["serverOrigin"] != "https://chat.example.test" {
		t.Fatalf("data = %v", message.Data)
	}

	if env.oauth.count() != 1 {
		t.Fatalf("OAuth token requests = %d, want the cached token reused", env.oauth.count())
	}
}

func TestFCMResponseClassification(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantGone bool
	}{
		{"unregistered", http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`, true},
		{"sender mismatch", http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"SENDER_ID_MISMATCH"}]}}`, true},
		{"invalid token", http.StatusBadRequest, `{"error":{"code":400,"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"},{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.token","description":"Invalid registration token"}]}]}}`, true},
		{"invalid message field", http.StatusBadRequest, `{"error":{"code":400,"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"},{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.android.ttl"}]}]}}`, false},
		{"quota exceeded", http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"QUOTA_EXCEEDED"}]}}`, false},
		{"unavailable", http.StatusServiceUnavailable, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newNativeTestEnv(t, nil)
			env.fcm.respond(tt.status, tt.body)
			result := env.sender.Send(context.Background(), fcmDevice(), messagePayload(time.Time{}))
			if result.Success || result.Gone != tt.wantGone || result.Error == nil {
				t.Fatalf("Send = %+v, want gone=%v with an error", result, tt.wantGone)
			}
		})
	}
}

func TestNativeSendExpiredDeadlineContactsNoProvider(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	result := env.sender.Send(context.Background(), apnsDevice(false), messagePayload(env.clock.Now().Add(-time.Second)))
	if result.Success || result.Error == nil {
		t.Fatalf("Send = %+v, want a deadline error", result)
	}
	if env.production.count() != 0 {
		t.Fatal("expired payload contacted APNs")
	}
}

func TestNativeSendDisabledPlatform(t *testing.T) {
	env := newNativeTestEnv(t, func(cfg *config.PushConfig) {
		cfg.FCM = config.FCMConfig{}
	})
	if env.sender.Supports(runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM) {
		t.Fatal("sender supports disabled FCM")
	}
	result := env.sender.Send(context.Background(), fcmDevice(), messagePayload(time.Time{}))
	if result.Success || result.Error == nil {
		t.Fatalf("Send to disabled platform = %+v, want an error", result)
	}
}

func TestNativeSendToManyBoundsFanOut(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	devices := make([]*runtimestatev1.NativePushDevice, maxNativeDevicesPerSend+4)
	for i := range devices {
		devices[i] = apnsDevice(false)
	}
	results := env.sender.SendToMany(context.Background(), devices, func(*runtimestatev1.NativePushDevice) *Payload {
		return messagePayload(time.Time{})
	})
	if len(results) != maxNativeDevicesPerSend || env.production.count() != maxNativeDevicesPerSend {
		t.Fatalf("fan-out = %d results, %d requests; want %d", len(results), env.production.count(), maxNativeDevicesPerSend)
	}
}

func apnsVoIPDevice(sandbox bool) *runtimestatev1.NativePushDevice {
	return &runtimestatev1.NativePushDevice{
		Platform:    runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP,
		Token:       testAPNsToken,
		AppId:       testAppID,
		ApnsSandbox: sandbox,
	}
}

func TestAPNsVoIPCallAndCallEnd(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	payloads := map[string]*Payload{
		"call":     BuildIncomingCallPayload("https://chat.example.test", "room-1", "call-1", "user-2", "Alice"),
		"call_end": BuildCallEndPayload("https://chat.example.test", "room-1", "call-1", "user-2"),
	}
	for kind, payload := range payloads {
		t.Run(kind, func(t *testing.T) {
			result := env.sender.Send(context.Background(), apnsVoIPDevice(true), payload)
			if !result.Success {
				t.Fatalf("Send = %+v, want success", result)
			}
			request := env.sandbox.last(t)
			if request.path != "/3/device/"+testAPNsToken {
				t.Fatalf("path = %q", request.path)
			}
			for header, want := range map[string]string{
				"apns-topic":     testAppID + ".voip",
				"apns-push-type": "voip",
				"apns-priority":  "10",
			} {
				if got := request.header.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if request.header.Get("apns-collapse-id") != "" {
				t.Error("VoIP push has a collapse ID")
			}
			expiration, err := strconv.ParseInt(request.header.Get("apns-expiration"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			if want := env.clock.Now().Add(callAlertTTL).Unix(); expiration != want {
				t.Fatalf("apns-expiration = %d, want %d", expiration, want)
			}
			var body map[string]any
			if err := json.Unmarshal(request.body, &body); err != nil {
				t.Fatal(err)
			}
			if aps, ok := body["aps"].(map[string]any); !ok || len(aps) != 0 {
				t.Fatalf("VoIP aps = %v, want an empty dictionary", body["aps"])
			}
			for key, want := range map[string]string{
				"kind":         kind,
				"callId":       "call-1",
				"groupId":      "room-1",
				"serverOrigin": "https://chat.example.test",
				"url":          "https://chat.example.test/chat/-/room-1",
				"recipientId":  "user-2",
			} {
				if body[key] != want {
					t.Errorf("data %s = %v, want %q", key, body[key], want)
				}
			}
			wantCaller := map[string]any{"call": "Alice", "call_end": nil}[kind]
			if body["callerName"] != wantCaller {
				t.Errorf("callerName = %v, want %v", body["callerName"], wantCaller)
			}
		})
	}
}

func TestNativeSendRefusesWrongKindForDevice(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	refused := []struct {
		name    string
		device  *runtimestatev1.NativePushDevice
		payload *Payload
	}{
		{"message to VoIP", apnsVoIPDevice(false), messagePayload(time.Time{})},
		{"test to VoIP", apnsVoIPDevice(false), BuildNativeTestPayload("https://chat.example.test", "user-1")},
		{"call end to APNs alert", apnsDevice(false), BuildCallEndPayload("https://chat.example.test", "room-1", "call-1", "user-2")},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			result := env.sender.Send(context.Background(), tt.device, tt.payload)
			if result.Success || result.Error == nil || result.Gone {
				t.Fatalf("Send = %+v, want a refusal", result)
			}
		})
	}
	if env.production.count() != 0 {
		t.Fatal("refused push contacted APNs")
	}
	if !env.sender.Supports(runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP) {
		t.Fatal("sender does not support APNs VoIP with APNs enabled")
	}
}

// fcmRequestBody is the part of an FCM v1 request that the call tests read.
type fcmRequestBody struct {
	Message struct {
		Notification map[string]string `json:"notification"`
		Data         map[string]string `json:"data"`
		Android      map[string]any    `json:"android"`
	} `json:"message"`
}

func TestFCMCallPushesAreDataOnly(t *testing.T) {
	env := newNativeTestEnv(t, nil)
	payloads := map[string]*Payload{
		"call":     BuildIncomingCallPayload("https://chat.example.test", "room-1", "call-1", "user-2", "Alice"),
		"call_end": BuildCallEndPayload("https://chat.example.test", "room-1", "call-1", "user-2"),
	}
	for kind, payload := range payloads {
		t.Run(kind, func(t *testing.T) {
			if result := env.sender.Send(context.Background(), fcmDevice(), payload); !result.Success {
				t.Fatalf("Send = %+v, want success", result)
			}
			var body fcmRequestBody
			if err := json.Unmarshal(env.fcm.last(t).body, &body); err != nil {
				t.Fatal(err)
			}
			if body.Message.Notification != nil {
				t.Fatalf("call push has a notification block: %v", body.Message.Notification)
			}
			if _, ok := body.Message.Android["notification"]; ok {
				t.Fatalf("call push has an Android notification block: %v", body.Message.Android)
			}
			if body.Message.Android["priority"] != "HIGH" || body.Message.Android["ttl"] != "30s" || body.Message.Android["restricted_package_name"] != testAppID {
				t.Fatalf("android = %v", body.Message.Android)
			}
			data := body.Message.Data
			if data["kind"] != kind || data["callId"] != "call-1" || data["groupId"] != "room-1" || data["recipientId"] != "user-2" ||
				data["serverOrigin"] != "https://chat.example.test" || data["url"] != "https://chat.example.test/chat/-/room-1" {
				t.Fatalf("data = %v", data)
			}
			if kind == "call" && data["callerName"] != "Alice" {
				t.Fatalf("callerName = %q, want Alice", data["callerName"])
			}
		})
	}
}

func TestNativeCallerNameWithoutPreviews(t *testing.T) {
	disabled := false
	env := newNativeTestEnv(t, func(cfg *config.PushConfig) {
		cfg.APNs.IncludePreviews = &disabled
		cfg.FCM.IncludePreviews = &disabled
	})
	call := BuildIncomingCallPayload("https://chat.example.test", "room-1", "call-1", "user-2", "Alice")

	env.sender.Send(context.Background(), apnsVoIPDevice(false), call)
	env.sender.Send(context.Background(), fcmDevice(), call)
	for name, raw := range map[string][]byte{"APNs VoIP": env.production.last(t).body, "FCM": env.fcm.last(t).body} {
		if strings.Contains(string(raw), "Alice") {
			t.Errorf("%s call push contains the caller name: %s", name, raw)
		}
		if !strings.Contains(string(raw), `"callerName":"`+nativeGenericCallTitle+`"`) {
			t.Errorf("%s call push has no generic callerName: %s", name, raw)
		}
	}
}

func TestNativeDeviceSelection(t *testing.T) {
	otherApp := "mn.setunari.other"
	apns := apnsDevice(false)
	apnsOther := &runtimestatev1.NativePushDevice{Platform: runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS, Token: "aa" + testAPNsToken[2:], AppId: otherApp}
	voip := apnsVoIPDevice(false)
	fcm := fcmDevice()
	all := []*runtimestatev1.NativePushDevice{apns, apnsOther, voip, fcm}

	tokens := func(devices []*runtimestatev1.NativePushDevice) []string {
		var out []string
		for _, device := range devices {
			out = append(out, device.GetPlatform().String()+"/"+device.GetAppId())
		}
		return out
	}
	tests := []struct {
		name string
		got  []*runtimestatev1.NativePushDevice
		want []*runtimestatev1.NativePushDevice
	}{
		{"alerts skip VoIP", AlertDevices(all), []*runtimestatev1.NativePushDevice{apns, apnsOther, fcm}},
		{"call rings VoIP, FCM, and APNs without VoIP", CallRingDevices(all), []*runtimestatev1.NativePushDevice{apnsOther, voip, fcm}},
		{"call rings APNs alert as fallback", CallRingDevices([]*runtimestatev1.NativePushDevice{apns, fcm}), []*runtimestatev1.NativePushDevice{apns, fcm}},
		{"call end goes to VoIP and FCM", CallEndDevices(all), []*runtimestatev1.NativePushDevice{voip, fcm}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := strings.Join(tokens(tt.got), ","), strings.Join(tokens(tt.want), ","); got != want {
				t.Fatalf("selected = %s, want %s", got, want)
			}
		})
	}
}
