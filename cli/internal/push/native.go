package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/chattocorp/chatto/cli/internal/config"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

const (
	// nativeRequestTimeout bounds one provider request, including the OAuth
	// token request that FCM can need first.
	nativeRequestTimeout = 10 * time.Second
	// maxNativeDevicesPerSend bounds one native fan-out. It matches the
	// per-account device limit.
	maxNativeDevicesPerSend = 16
	// callAlertTTL is how long APNs and FCM keep an undelivered call or
	// call-end push. It matches the ring duration of the incoming-call banner.
	callAlertTTL = 30 * time.Second
	// defaultNativeTTL is the provider retention of a native push without an
	// explicit deadline, such as a test notification.
	defaultNativeTTL = 24 * time.Hour
	// maxNativeResponseBody is the largest provider response body that the
	// sender reads to classify an error.
	maxNativeResponseBody = 16 * 1024
	// nativeGenericMessageTitle replaces private content when the operator
	// disables previews for a provider.
	nativeGenericMessageTitle = "New notification"
	// nativeGenericCallTitle replaces private call content when the operator
	// disables previews for a provider.
	nativeGenericCallTitle = "Incoming call"
)

// NativeSender delivers notifications to native mobile devices through APNs
// and FCM. A nil provider client means that the operator did not enable that
// provider. NativeSender is safe for concurrent use.
type NativeSender struct {
	apns         *apnsClient
	fcm          *fcmClient
	logger       *log.Logger
	requestSlots chan struct{}
	now          func() time.Time
}

// NativeSenderOptions replaces provider endpoints and the HTTP client. Tests
// use it to point the sender at fake providers. Production code uses the zero
// value.
type NativeSenderOptions struct {
	// HTTPClient sends provider and OAuth requests. Default: an HTTP/2-capable
	// client that does not follow redirects.
	HTTPClient *http.Client
	// APNsProductionURL replaces https://api.push.apple.com.
	APNsProductionURL string
	// APNsSandboxURL replaces https://api.sandbox.push.apple.com.
	APNsSandboxURL string
	// FCMBaseURL replaces https://fcm.googleapis.com.
	FCMBaseURL string
	// FCMTokenURL replaces the token_uri of the service account key.
	FCMTokenURL string
	// Now replaces the clock.
	Now func() time.Time
}

// NewNativeSender creates a sender for the enabled native push providers. It
// returns nil and no error when no native provider is enabled. It returns an
// error when an enabled provider has unusable key material, so startup fails
// instead of accepting registrations that cannot receive pushes.
func NewNativeSender(cfg config.PushConfig, logger *log.Logger) (*NativeSender, error) {
	return NewNativeSenderWithOptions(cfg, logger, NativeSenderOptions{})
}

// NewNativeSenderWithOptions is NewNativeSender with replaced endpoints.
func NewNativeSenderWithOptions(cfg config.PushConfig, logger *log.Logger, opts NativeSenderOptions) (*NativeSender, error) {
	if !cfg.NativeConfigured() {
		return nil, nil
	}
	if logger == nil {
		logger = log.Default()
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = newNativeHTTPClient()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sender := &NativeSender{
		logger:       logger,
		requestSlots: make(chan struct{}, maxConcurrentPushRequests),
		now:          now,
	}
	if cfg.APNs.Enabled {
		client, err := newAPNsClient(cfg.APNs, httpClient, opts, now)
		if err != nil {
			return nil, err
		}
		sender.apns = client
	}
	if cfg.FCM.Enabled {
		client, err := newFCMClient(cfg.FCM, httpClient, opts)
		if err != nil {
			return nil, err
		}
		sender.fcm = client
	}
	return sender, nil
}

// newNativeHTTPClient returns the client for APNs, FCM, and Google OAuth.
// These are fixed, operator-trusted hosts, so the client uses the environment
// proxy settings. It never follows redirects. APNs requires HTTP/2, which the
// transport negotiates through TLS ALPN.
func newNativeHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	return &http.Client{
		Transport: transport,
		Timeout:   nativeRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Supports reports whether the sender delivers to platform.
func (s *NativeSender) Supports(platform runtimestatev1.NativePushDevicePlatform) bool {
	if s == nil {
		return false
	}
	switch platform {
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP:
		return s.apns != nil
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM:
		return s.fcm != nil
	default:
		return false
	}
}

// NativeSendResult is the result of one native push attempt.
type NativeSendResult struct {
	// Token is the device token of the attempt. Never log it; use
	// EndpointLogID.
	Token string
	// Success is true when the provider accepted the push.
	Success bool
	// Gone is true when the provider reported that the token is permanently
	// invalid. The caller removes the device.
	Gone bool
	// Error is a generic error without provider response content.
	Error error
}

// Send delivers payload to one native device. It refuses a payload that the
// device must not receive: APNs VoIP devices receive only call kinds, because
// iOS requires the app to report every VoIP push to CallKit, and plain APNs
// devices receive no KindCallEnd, because an alert push cannot stop a ring.
func (s *NativeSender) Send(ctx context.Context, device *runtimestatev1.NativePushDevice, payload *Payload) *NativeSendResult {
	result := &NativeSendResult{Token: device.GetToken()}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, nativeRequestTimeout)
	defer cancel()

	select {
	case s.requestSlots <- struct{}{}:
		defer func() { <-s.requestSlots }()
	case <-requestCtx.Done():
		result.Error = requestCtx.Err()
		return result
	}

	expiresAt, deliverable := nativeExpiry(payload, s.now())
	if !deliverable {
		result.Error = context.DeadlineExceeded
		return result
	}

	var outcome nativeOutcome
	switch device.GetPlatform() {
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS:
		if s.apns == nil {
			result.Error = errors.New("native push delivery failed: APNs is not enabled")
			return result
		}
		if payload.kindOrDefault() == KindCallEnd {
			result.Error = errors.New("native push delivery failed: APNs alert devices do not receive call-end pushes")
			return result
		}
		outcome = s.apns.send(requestCtx, device, payload, expiresAt)
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP:
		if s.apns == nil {
			result.Error = errors.New("native push delivery failed: APNs is not enabled")
			return result
		}
		if !payload.kindOrDefault().IsCallKind() {
			result.Error = errors.New("native push delivery failed: APNs VoIP devices receive only call pushes")
			return result
		}
		outcome = s.apns.sendVoIP(requestCtx, device, payload, expiresAt)
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM:
		if s.fcm == nil {
			result.Error = errors.New("native push delivery failed: FCM is not enabled")
			return result
		}
		outcome = s.fcm.send(requestCtx, device, payload, expiresAt.Sub(s.now()))
	default:
		result.Error = errors.New("native push delivery failed: unknown platform")
		return result
	}
	result.Success = outcome.success
	result.Gone = outcome.gone
	result.Error = outcome.err
	return result
}

// SendToMany delivers a device-specific payload to each device, with bounded
// concurrency. It attempts at most maxNativeDevicesPerSend devices.
func (s *NativeSender) SendToMany(
	ctx context.Context,
	devices []*runtimestatev1.NativePushDevice,
	payloadFor func(*runtimestatev1.NativePushDevice) *Payload,
) []*NativeSendResult {
	if len(devices) > maxNativeDevicesPerSend {
		devices = devices[:maxNativeDevicesPerSend]
	}
	results := make([]*NativeSendResult, len(devices))
	if len(devices) == 0 {
		return results
	}
	workerCount := min(len(devices), maxConcurrentPushRequests)
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for i := range jobs {
				results[i] = s.Send(ctx, devices[i], payloadFor(devices[i]))
			}
		}()
	}
	for i := range devices {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return results
}

// nativeOutcome is the provider-specific result of one request.
type nativeOutcome struct {
	success bool
	gone    bool
	err     error
}

// nativeExpiry returns the absolute time after which the provider must drop
// the push. Call and call-end pushes use callAlertTTL. An explicit delivery deadline, such as the
// notification alert window, can shorten that time. It returns false when the
// deadline already passed.
func nativeExpiry(payload *Payload, now time.Time) (time.Time, bool) {
	expiresAt := now.Add(defaultNativeTTL)
	if payload.kindOrDefault().IsCallKind() {
		expiresAt = now.Add(callAlertTTL)
	}
	if payload.TTLSeconds > 0 {
		if ttl := now.Add(time.Duration(payload.TTLSeconds) * time.Second); ttl.Before(expiresAt) {
			expiresAt = ttl
		}
	}
	if !payload.DeliveryDeadline.IsZero() {
		if !payload.DeliveryDeadline.After(now) {
			return time.Time{}, false
		}
		if payload.DeliveryDeadline.Before(expiresAt) {
			expiresAt = payload.DeliveryDeadline
		}
	}
	return expiresAt, true
}

// nativeContent is the visible title and body of a native push after the
// preview setting of the provider.
func nativeContent(payload *Payload, includePreviews bool) (title, body string) {
	if includePreviews || payload.kindOrDefault() == KindTest {
		return payload.Title, payload.Body
	}
	if payload.kindOrDefault() == KindCall {
		return nativeGenericCallTitle, ""
	}
	return nativeGenericMessageTitle, ""
}

// nativeCallerName returns the callerName data value of a KindCall push after
// the preview setting of the provider.
func nativeCallerName(payload *Payload, includePreviews bool) string {
	if !includePreviews || payload.CallerName == "" {
		return nativeGenericCallTitle
	}
	return payload.CallerName
}

// nativeData returns the custom data that the mobile app reads when it
// receives the push or when the user opens the notification. It contains
// opaque IDs and URLs, but no message content. A KindCall push also contains
// callerName, which is the caller display name only when includePreviews is
// true.
func nativeData(payload *Payload, includePreviews bool) map[string]string {
	data := map[string]string{"kind": string(payload.kindOrDefault())}
	set := func(key, value string) {
		if value != "" {
			data[key] = value
		}
	}
	set("serverOrigin", payload.ServerOrigin)
	set("recipientId", payload.RecipientID)
	set("url", payload.URL)
	set("notificationId", payload.NotificationID)
	set("attentionLevel", payload.AttentionLevel)
	set("tag", payload.Tag)
	set("groupId", payload.GroupID)
	set("callId", payload.CallID)
	if payload.kindOrDefault() == KindCall {
		data["callerName"] = nativeCallerName(payload, includePreviews)
	}
	return data
}

// readNativeResponseBody reads a bounded provider response body. The body is
// used only to classify the error; it is never returned or logged.
func readNativeResponseBody(body io.Reader) []byte {
	data, _ := io.ReadAll(io.LimitReader(body, maxNativeResponseBody))
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	return data
}

// nativeRequestError maps a transport error to a generic error that contains
// no provider or network details.
func nativeRequestError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("native push delivery request failed")
	}
}

func nativeStatusError(provider string, statusCode int, reason string) error {
	if reason == "" {
		return fmt.Errorf("%s returned status %d", provider, statusCode)
	}
	return fmt.Errorf("%s returned status %d (%s)", provider, statusCode, reason)
}

// providerReasonLabel returns a provider error reason only when it is a short
// identifier, so a provider response cannot put arbitrary text into errors or
// logs.
func providerReasonLabel(reason string) string {
	if len(reason) == 0 || len(reason) > 64 {
		return ""
	}
	for _, r := range reason {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ""
		}
	}
	return reason
}

// serverOrigin returns the scheme and host of baseURL, without a path.
func serverOrigin(baseURL string) string {
	serverURL, err := url.Parse(baseURL)
	if err != nil || serverURL.Host == "" {
		return ""
	}
	return serverURL.Scheme + "://" + serverURL.Host
}

// BuildNativeTestPayload creates the native test notification for a user.
// It contains no private content, so the preview setting does not change it.
func BuildNativeTestPayload(baseURL, recipientID string) *Payload {
	return &Payload{
		Title:        "Test notification",
		Body:         "Push notifications are working.",
		URL:          buildAppURL(baseURL, []string{"chat", "-"}, "", ""),
		Tag:          "push-test",
		ServerOrigin: serverOrigin(baseURL),
		RecipientID:  recipientID,
		Kind:         KindTest,
	}
}

// BuildIncomingCallPayload creates the native push that rings a recipient
// for a call in roomID. Clicking it opens the room, where the client shows
// the incoming-call banner while the call rings.
func BuildIncomingCallPayload(baseURL, roomID, callID, recipientID, callerName string) *Payload {
	return &Payload{
		Title:          fmt.Sprintf("%s is calling you", callerName),
		CallerName:     callerName,
		Body:           "Open Chatto to join the call.",
		URL:            buildNotificationURL(buildAppURL(baseURL, []string{"chat", "-"}, "", ""), roomID, "", ""),
		Tag:            "call-" + callID,
		ServerOrigin:   serverOrigin(baseURL),
		RecipientID:    recipientID,
		AttentionLevel: "important",
		Kind:           KindCall,
		GroupID:        roomID,
		CallID:         callID,
	}
}

// BuildCallEndPayload creates the native push that stops the ring of an
// earlier BuildIncomingCallPayload push, because the call ended or because
// the recipient joined it on another device. It contains no visible content.
func BuildCallEndPayload(baseURL, roomID, callID, recipientID string) *Payload {
	return &Payload{
		URL:          buildNotificationURL(buildAppURL(baseURL, []string{"chat", "-"}, "", ""), roomID, "", ""),
		Tag:          "call-end-" + callID,
		ServerOrigin: serverOrigin(baseURL),
		RecipientID:  recipientID,
		Kind:         KindCallEnd,
		GroupID:      roomID,
		CallID:       callID,
	}
}

// AlertDevices returns the devices that can receive a visible alert, such as a
// message or a test notification. It removes APNs VoIP devices, which receive
// only call pushes.
func AlertDevices(devices []*runtimestatev1.NativePushDevice) []*runtimestatev1.NativePushDevice {
	selected := make([]*runtimestatev1.NativePushDevice, 0, len(devices))
	for _, device := range devices {
		if device.GetPlatform() != runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP {
			selected = append(selected, device)
		}
	}
	return selected
}

// CallRingDevices returns the devices that receive a KindCall push: every
// APNs VoIP and FCM device, and a plain APNs device only when the account has
// no APNs VoIP device for the same app ID. The VoIP device rings through
// CallKit, so the time-sensitive alert on the same app would duplicate it.
func CallRingDevices(devices []*runtimestatev1.NativePushDevice) []*runtimestatev1.NativePushDevice {
	voipApps := make(map[string]bool)
	for _, device := range devices {
		if device.GetPlatform() == runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP {
			voipApps[device.GetAppId()] = true
		}
	}
	selected := make([]*runtimestatev1.NativePushDevice, 0, len(devices))
	for _, device := range devices {
		switch device.GetPlatform() {
		case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS:
			if !voipApps[device.GetAppId()] {
				selected = append(selected, device)
			}
		case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP,
			runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM:
			selected = append(selected, device)
		}
	}
	return selected
}

// CallEndDevices returns the devices that receive a KindCallEnd push: the APNs
// VoIP and FCM devices. A plain APNs alert cannot stop a ring, so those
// devices get nothing.
func CallEndDevices(devices []*runtimestatev1.NativePushDevice) []*runtimestatev1.NativePushDevice {
	selected := make([]*runtimestatev1.NativePushDevice, 0, len(devices))
	for _, device := range devices {
		switch device.GetPlatform() {
		case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP,
			runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM:
			selected = append(selected, device)
		}
	}
	return selected
}
