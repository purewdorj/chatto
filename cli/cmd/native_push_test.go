package cmd

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/chattocorp/chatto/cli/internal/config"
	"github.com/chattocorp/chatto/cli/internal/core"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
	"github.com/chattocorp/chatto/cli/internal/push"
)

const (
	nativeHandlerGoneToken     = "1111111111111111111111111111111111111111111111111111111111111111"
	nativeHandlerAcceptedToken = "fcm-accepted:token"
	nativeHandlerCleanupToken  = "native-handler-cleanup-token-0123456789abcdef"
	nativeHandlerAppID         = "mn.setunari.chatto.mobile"
)

// recordingNativePushSender records native sends and answers with results.
type recordingNativePushSender struct {
	mu       sync.Mutex
	calls    int
	devices  [][]*runtimestatev1.NativePushDevice
	payloads []*push.Payload
	results  func([]*runtimestatev1.NativePushDevice) []*push.NativeSendResult
}

func (s *recordingNativePushSender) SendToMany(
	_ context.Context,
	devices []*runtimestatev1.NativePushDevice,
	payloadFor func(*runtimestatev1.NativePushDevice) *push.Payload,
) []*push.NativeSendResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.devices = append(s.devices, devices)
	for _, device := range devices {
		payload := *payloadFor(device)
		s.payloads = append(s.payloads, &payload)
	}
	return s.results(devices)
}

func saveNativeHandlerDevices(t *testing.T, ctx context.Context, chattoCore *core.ChattoCore, userID string) {
	t.Helper()
	registrations := []core.NativePushDeviceRegistration{
		{
			Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
			Token:        nativeHandlerGoneToken,
			AppID:        nativeHandlerAppID,
			CleanupToken: nativeHandlerCleanupToken,
		},
		{
			Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM,
			Token:        nativeHandlerAcceptedToken,
			AppID:        nativeHandlerAppID,
			CleanupToken: nativeHandlerCleanupToken,
		},
	}
	for _, registration := range registrations {
		if _, err := chattoCore.SaveNativePushDevice(ctx, userID, registration); err != nil {
			t.Fatalf("SaveNativePushDevice: %v", err)
		}
	}
}

func goneAndAcceptedResults(devices []*runtimestatev1.NativePushDevice) []*push.NativeSendResult {
	results := make([]*push.NativeSendResult, 0, len(devices))
	for _, device := range devices {
		if device.GetToken() == nativeHandlerGoneToken {
			results = append(results, &push.NativeSendResult{Token: device.GetToken(), Gone: true, Error: errors.New("gone")})
		} else {
			results = append(results, &push.NativeSendResult{Token: device.GetToken(), Success: true})
		}
	}
	return results
}

func TestNotificationAlertHandlerDeliversToNativeDevices(t *testing.T) {
	chattoCore, ctx, alice, _, occurrence := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	native := &recordingNativePushSender{results: goneAndAcceptedResults}

	// Web Push is not configured: native push must work without it.
	handler := notificationAlertHandler(chattoCore, config.ChattoConfig{
		Webserver: config.WebserverConfig{URL: "https://chat.example.test"},
	}, nil, native, log.New(io.Discard))
	if err := handler(ctx, occurrence); err != nil {
		t.Fatalf("notification alert handler: %v", err)
	}
	if native.calls != 1 || len(native.payloads) != 2 {
		t.Fatalf("native calls/payloads = %d/%d, want one call for two devices", native.calls, len(native.payloads))
	}
	payload := native.payloads[0]
	if payload.Kind != push.KindMessage || payload.DeliveryDeadline.IsZero() || payload.Title == "" {
		t.Fatalf("native payload = %+v", payload)
	}
	roomID := occurrence.GetSignal().GetDirectMessageReceived().GetMessage().GetRoomId()
	if payload.GroupID != roomID || payload.URL != "https://chat.example.test/chat/-/"+roomID || payload.ServerOrigin != "https://chat.example.test" {
		t.Fatalf("native payload routing = %+v", payload)
	}

	devices, err := chattoCore.GetUserNativePushDevices(ctx, alice.Id)
	if err != nil {
		t.Fatalf("GetUserNativePushDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].GetToken() != nativeHandlerAcceptedToken {
		t.Fatalf("remaining devices = %+v, want only the accepted device", devices)
	}
}

func TestNotificationAlertHandlerReportsNativeFailure(t *testing.T) {
	chattoCore, ctx, alice, _, occurrence := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	native := &recordingNativePushSender{results: func(devices []*runtimestatev1.NativePushDevice) []*push.NativeSendResult {
		results := make([]*push.NativeSendResult, len(devices))
		for i, device := range devices {
			results[i] = &push.NativeSendResult{Token: device.GetToken(), Error: errors.New("provider unavailable")}
		}
		return results
	}}
	err := notificationAlertHandler(chattoCore, config.ChattoConfig{}, nil, native, log.New(io.Discard))(ctx, occurrence)
	if err == nil || errors.Is(err, core.ErrNotificationAlertSuppressed) {
		t.Fatalf("handler = %v, want a transport error for retry", err)
	}
	devices, err := chattoCore.GetUserNativePushDevices(ctx, alice.Id)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices after transient failure = (%d, %v), want both kept", len(devices), err)
	}
}

func TestNotificationAlertHandlerSuppressesWithoutDevices(t *testing.T) {
	chattoCore, ctx, _, _, occurrence := notificationPushFixture(t, 1)
	native := &recordingNativePushSender{results: func([]*runtimestatev1.NativePushDevice) []*push.NativeSendResult {
		t.Fatal("handler contacted a provider without devices")
		return nil
	}}
	err := notificationAlertHandler(chattoCore, config.ChattoConfig{}, nil, native, log.New(io.Discard))(ctx, occurrence)
	if !errors.Is(err, core.ErrNotificationAlertSuppressed) {
		t.Fatalf("handler = %v, want suppressed", err)
	}
}

func TestPushTestHandlerIncludesNativeDevices(t *testing.T) {
	chattoCore, ctx, alice, _, _ := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	native := &recordingNativePushSender{results: goneAndAcceptedResults}
	handler := pushTestHandler(chattoCore, config.ChattoConfig{
		Webserver: config.WebserverConfig{URL: "https://chat.example.test"},
	}, nil, native, log.New(io.Discard))
	if err := handler(ctx, alice.Id); err != nil {
		t.Fatalf("push test handler: %v", err)
	}
	if len(native.payloads) != 2 || native.payloads[0].Kind != push.KindTest {
		t.Fatalf("test payloads = %+v", native.payloads)
	}
	if err := pushTestHandler(chattoCore, config.ChattoConfig{}, nil, native, log.New(io.Discard))(ctx, "user-without-devices"); err == nil {
		t.Fatal("push test without devices returned no error")
	}
}

func TestIncomingCallAlertHandlerRingsDMMember(t *testing.T) {
	chattoCore, ctx, alice, bob, occurrence := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	roomID := occurrence.GetSignal().GetDirectMessageReceived().GetMessage().GetRoomId()
	native := &recordingNativePushSender{results: goneAndAcceptedResults}
	handler := incomingCallAlertHandler(chattoCore, config.ChattoConfig{
		Webserver: config.WebserverConfig{URL: "https://chat.example.test"},
	}, native, log.New(io.Discard))

	if err := chattoCore.JoinVoiceCall(ctx, bob.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall: %v", err)
	}
	snapshot, err := chattoCore.GetCallSnapshot(roomID)
	if err != nil {
		t.Fatalf("GetCallSnapshot: %v", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	handler(callCtx, roomID, snapshot.Call.CallID, bob.Id)

	if native.calls != 1 || len(native.payloads) != 2 {
		t.Fatalf("native calls/payloads = %d/%d, want one ring for two devices", native.calls, len(native.payloads))
	}
	payload := native.payloads[0]
	if payload.Kind != push.KindCall || payload.CallID != snapshot.Call.CallID || payload.RecipientID != alice.Id || payload.GroupID != roomID {
		t.Fatalf("call payload = %+v", payload)
	}
	devices, err := chattoCore.GetUserNativePushDevices(ctx, alice.Id)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices after ring = (%d, %v), want the gone device removed", len(devices), err)
	}

	// After the recipient joins, a late handler run does not ring.
	if err := chattoCore.JoinVoiceCall(ctx, alice.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall alice: %v", err)
	}
	handler(callCtx, roomID, snapshot.Call.CallID, bob.Id)
	if native.calls != 1 {
		t.Fatalf("native calls after join = %d, want no new ring", native.calls)
	}
}

const nativeHandlerVoIPToken = "2222222222222222222222222222222222222222222222222222222222222222"

func saveNativeHandlerVoIPDevice(t *testing.T, ctx context.Context, chattoCore *core.ChattoCore, userID string) {
	t.Helper()
	if _, err := chattoCore.SaveNativePushDevice(ctx, userID, core.NativePushDeviceRegistration{
		Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP,
		Token:        nativeHandlerVoIPToken,
		AppID:        nativeHandlerAppID,
		CleanupToken: nativeHandlerCleanupToken,
	}); err != nil {
		t.Fatalf("SaveNativePushDevice VoIP: %v", err)
	}
}

func acceptAll(devices []*runtimestatev1.NativePushDevice) []*push.NativeSendResult {
	results := make([]*push.NativeSendResult, len(devices))
	for i, device := range devices {
		results[i] = &push.NativeSendResult{Token: device.GetToken(), Success: true}
	}
	return results
}

// sentPlatforms returns the platform of each device that the sender received.
func (s *recordingNativePushSender) sentPlatforms() map[runtimestatev1.NativePushDevicePlatform]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	platforms := make(map[runtimestatev1.NativePushDevicePlatform]int)
	for _, devices := range s.devices {
		for _, device := range devices {
			platforms[device.GetPlatform()]++
		}
	}
	return platforms
}

func TestPushTestAndAlertsSkipVoIPDevices(t *testing.T) {
	chattoCore, ctx, alice, _, occurrence := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	saveNativeHandlerVoIPDevice(t, ctx, chattoCore, alice.Id)
	cfg := config.ChattoConfig{Webserver: config.WebserverConfig{URL: "https://chat.example.test"}}

	testSender := &recordingNativePushSender{results: acceptAll}
	if err := pushTestHandler(chattoCore, cfg, nil, testSender, log.New(io.Discard))(ctx, alice.Id); err != nil {
		t.Fatalf("push test handler: %v", err)
	}
	alertSender := &recordingNativePushSender{results: acceptAll}
	if err := notificationAlertHandler(chattoCore, cfg, nil, alertSender, log.New(io.Discard))(ctx, occurrence); err != nil {
		t.Fatalf("notification alert handler: %v", err)
	}
	for name, sender := range map[string]*recordingNativePushSender{"test": testSender, "alert": alertSender} {
		platforms := sender.sentPlatforms()
		if platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP] != 0 || len(sender.payloads) != 2 {
			t.Fatalf("%s sends = %v, want APNs and FCM only", name, platforms)
		}
	}

	// An account with only a VoIP device has no device for a test notification.
	if err := chattoCore.DeleteNativePushDevice(ctx, alice.Id, nativeHandlerGoneToken); err != nil {
		t.Fatalf("DeleteNativePushDevice: %v", err)
	}
	if err := chattoCore.DeleteNativePushDevice(ctx, alice.Id, nativeHandlerAcceptedToken); err != nil {
		t.Fatalf("DeleteNativePushDevice: %v", err)
	}
	onlyVoIP := &recordingNativePushSender{results: acceptAll}
	if err := pushTestHandler(chattoCore, cfg, nil, onlyVoIP, log.New(io.Discard))(ctx, alice.Id); err == nil || onlyVoIP.calls != 0 {
		t.Fatalf("push test with only a VoIP device = (%v, %d calls), want an error without a send", err, onlyVoIP.calls)
	}
}

func TestIncomingCallUsesVoIPAndStopsRing(t *testing.T) {
	chattoCore, ctx, alice, bob, occurrence := notificationPushFixture(t, 1)
	saveNativeHandlerDevices(t, ctx, chattoCore, alice.Id)
	saveNativeHandlerVoIPDevice(t, ctx, chattoCore, alice.Id)
	roomID := occurrence.GetSignal().GetDirectMessageReceived().GetMessage().GetRoomId()
	cfg := config.ChattoConfig{Webserver: config.WebserverConfig{URL: "https://chat.example.test"}}
	ringSender := &recordingNativePushSender{results: acceptAll}
	ring := incomingCallAlertHandler(chattoCore, cfg, ringSender, log.New(io.Discard))
	endSender := &recordingNativePushSender{results: acceptAll}
	end := callRingStoppedHandler(chattoCore, cfg, endSender, log.New(io.Discard))
	ended := make(chan struct{}, 2)
	chattoCore.SetCallRingStoppedHandler(func(ctx context.Context, roomID, callID string, recipientIDs []string) {
		end(ctx, roomID, callID, recipientIDs)
		ended <- struct{}{}
	})
	t.Cleanup(func() { chattoCore.SetCallRingStoppedHandler(nil) })

	if err := chattoCore.JoinVoiceCall(ctx, bob.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall: %v", err)
	}
	snapshot, err := chattoCore.GetCallSnapshot(roomID)
	if err != nil {
		t.Fatalf("GetCallSnapshot: %v", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ring(callCtx, roomID, snapshot.Call.CallID, bob.Id)

	// The VoIP device replaces the APNs alert of the same app.
	platforms := ringSender.sentPlatforms()
	if platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS] != 0 ||
		platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP] != 1 ||
		platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM] != 1 {
		t.Fatalf("ring platforms = %v, want VoIP and FCM", platforms)
	}
	if payload := ringSender.payloads[0]; payload.Kind != push.KindCall || payload.CallerName == "" {
		t.Fatalf("ring payload = %+v", payload)
	}

	// The recipient joins on one device: the other devices stop ringing.
	if err := chattoCore.JoinVoiceCall(ctx, alice.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall alice: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("call-end handler did not run after the recipient joined")
	}
	platforms = endSender.sentPlatforms()
	if platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS] != 0 ||
		platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP] != 1 ||
		platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM] != 1 {
		t.Fatalf("call-end platforms = %v, want VoIP and FCM", platforms)
	}
	payload := endSender.payloads[0]
	if payload.Kind != push.KindCallEnd || payload.CallID != snapshot.Call.CallID || payload.RecipientID != alice.Id || payload.GroupID != roomID {
		t.Fatalf("call-end payload = %+v", payload)
	}
}

func TestIncomingCallFallsBackToAPNsAlertWithoutVoIP(t *testing.T) {
	chattoCore, ctx, alice, bob, occurrence := notificationPushFixture(t, 1)
	if _, err := chattoCore.SaveNativePushDevice(ctx, alice.Id, core.NativePushDeviceRegistration{
		Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		Token:        nativeHandlerGoneToken,
		AppID:        nativeHandlerAppID,
		CleanupToken: nativeHandlerCleanupToken,
	}); err != nil {
		t.Fatalf("SaveNativePushDevice: %v", err)
	}
	roomID := occurrence.GetSignal().GetDirectMessageReceived().GetMessage().GetRoomId()
	cfg := config.ChattoConfig{Webserver: config.WebserverConfig{URL: "https://chat.example.test"}}
	ringSender := &recordingNativePushSender{results: acceptAll}
	stopped := make(chan []string, 2)
	chattoCore.SetCallRingStoppedHandler(func(_ context.Context, _, _ string, recipientIDs []string) {
		stopped <- recipientIDs
	})
	t.Cleanup(func() { chattoCore.SetCallRingStoppedHandler(nil) })

	if err := chattoCore.JoinVoiceCall(ctx, bob.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall: %v", err)
	}
	snapshot, err := chattoCore.GetCallSnapshot(roomID)
	if err != nil {
		t.Fatalf("GetCallSnapshot: %v", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	incomingCallAlertHandler(chattoCore, cfg, ringSender, log.New(io.Discard))(callCtx, roomID, snapshot.Call.CallID, bob.Id)
	if platforms := ringSender.sentPlatforms(); platforms[runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS] != 1 {
		t.Fatalf("ring platforms = %v, want the APNs alert fallback", platforms)
	}

	// An APNs alert cannot stop a ring, so the server records no ring and
	// sends no call end.
	if err := chattoCore.JoinVoiceCall(ctx, alice.Id, roomID); err != nil {
		t.Fatalf("JoinVoiceCall alice: %v", err)
	}
	select {
	case recipients := <-stopped:
		t.Fatalf("ring stopped for %v, want no recorded ring", recipients)
	case <-time.After(300 * time.Millisecond):
	}
}
