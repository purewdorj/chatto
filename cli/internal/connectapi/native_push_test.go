package connectapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/chattocorp/chatto/cli/internal/config"
	apiv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/api/v1"
	authv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/auth/v1"
)

const (
	nativeAPITestToken   = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	nativeAPITestAppID   = "mn.setunari.chatto.mobile"
	nativeAPITestCleanup = "native-api-cleanup-token-0123456789abcdef"
)

func nativeRegisterRequest(platform apiv1.NativePushPlatform, token, appID string) *connect.Request[apiv1.RegisterNativeDeviceRequest] {
	return connect.NewRequest(&apiv1.RegisterNativeDeviceRequest{
		Platform:     platform,
		Token:        token,
		AppId:        appID,
		ApnsSandbox:  true,
		DeviceName:   new("Test iPhone"),
		CleanupToken: nativeAPITestCleanup,
	})
}

func TestRegisterNativeDevice(t *testing.T) {
	env := newConnectAPITestEnv(t)
	ctx := withCaller(env.ctx, env.viewer)
	apns := apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS

	if _, err := env.push.RegisterNativeDevice(env.ctx, nativeRegisterRequest(apns, nativeAPITestToken, nativeAPITestAppID)); errorCode(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated register code = %v", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apns, nativeAPITestToken, nativeAPITestAppID)); errorCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("register without native push code = %v, want failed_precondition", errorCode(err))
	}

	// APNs only; Web Push stays off.
	env.api.config.Push = config.PushConfig{APNs: config.APNsConfig{Enabled: true, Topics: []string{nativeAPITestAppID}}}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_FCM, "fcm-token", nativeAPITestAppID)); errorCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("register disabled FCM code = %v, want failed_precondition", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apns, nativeAPITestToken, "com.example.other")); errorCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("register disallowed app code = %v, want invalid_argument", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_UNSPECIFIED, nativeAPITestToken, nativeAPITestAppID)); errorCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("register unspecified platform code = %v, want invalid_argument", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apns, nativeAPITestToken, nativeAPITestAppID)); err != nil {
		t.Fatalf("RegisterNativeDevice: %v", err)
	}
	devices, err := env.core.GetUserNativePushDevices(env.ctx, env.viewer.Id)
	if err != nil || len(devices) != 1 {
		t.Fatalf("stored devices = (%d, %v), want one", len(devices), err)
	}
	if device := devices[0]; !device.GetApnsSandbox() || device.GetDeviceName() != "Test iPhone" || device.GetAppId() != nativeAPITestAppID {
		t.Fatalf("stored device = %+v", device)
	}

	runtimeResp, err := env.serverState.GetRuntimeConfig(ctx, connect.NewRequest(&apiv1.GetRuntimeConfigRequest{}))
	if err != nil {
		t.Fatalf("GetRuntimeConfig: %v", err)
	}
	runtime := runtimeResp.Msg.GetRuntime()
	if platforms := runtime.GetNativePushPlatforms(); len(platforms) != 2 || platforms[0] != apns || platforms[1] != apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP || runtime.GetPushNotificationsEnabled() || runtime.VapidPublicKey != nil {
		t.Fatalf("runtime push fields = %v / %v / %v", platforms, runtime.GetPushNotificationsEnabled(), runtime.VapidPublicKey)
	}

	if _, err := env.push.UnregisterNativeDevice(ctx, connect.NewRequest(&apiv1.UnregisterNativeDeviceRequest{Token: nativeAPITestToken})); err != nil {
		t.Fatalf("UnregisterNativeDevice: %v", err)
	}
	if _, err := env.push.UnregisterNativeDevice(ctx, connect.NewRequest(&apiv1.UnregisterNativeDeviceRequest{Token: nativeAPITestToken})); err != nil {
		t.Fatalf("second UnregisterNativeDevice: %v", err)
	}
	if devices, err := env.core.GetUserNativePushDevices(env.ctx, env.viewer.Id); err != nil || len(devices) != 0 {
		t.Fatalf("devices after unregister = (%d, %v), want none", len(devices), err)
	}
}

func TestRegisterNativeVoIPDevice(t *testing.T) {
	env := newConnectAPITestEnv(t)
	ctx := withCaller(env.ctx, env.viewer)
	voip := apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP
	const voipToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	// APNs VoIP needs APNs.
	env.api.config.Push = config.PushConfig{FCM: config.FCMConfig{Enabled: true, AppIDs: []string{nativeAPITestAppID}}}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(voip, voipToken, nativeAPITestAppID)); errorCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("register VoIP without APNs code = %v, want failed_precondition", errorCode(err))
	}

	// The VoIP device uses the bundle ID, not the ".voip" topic, in the APNs
	// topic allow-list.
	env.api.config.Push = config.PushConfig{APNs: config.APNsConfig{Enabled: true, Topics: []string{nativeAPITestAppID}}}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(voip, voipToken, nativeAPITestAppID+".voip")); errorCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("register VoIP topic as app ID code = %v, want invalid_argument", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(voip, "not-hex-token", nativeAPITestAppID)); errorCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("register non-hex VoIP token code = %v, want invalid_argument", errorCode(err))
	}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(voip, voipToken, nativeAPITestAppID)); err != nil {
		t.Fatalf("RegisterNativeDevice VoIP: %v", err)
	}
	devices, err := env.core.GetUserNativePushDevices(env.ctx, env.viewer.Id)
	if err != nil || len(devices) != 1 {
		t.Fatalf("stored devices = (%d, %v), want one", len(devices), err)
	}
	if device := devices[0]; device.GetPlatform().String() != "NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP" || !device.GetApnsSandbox() || device.GetAppId() != nativeAPITestAppID {
		t.Fatalf("stored VoIP device = %+v", device)
	}
}

func TestSendTestNotificationWithNativePushOnly(t *testing.T) {
	env := newConnectAPITestEnv(t)
	ctx := withCaller(env.ctx, env.viewer)
	env.api.config.Push = config.PushConfig{FCM: config.FCMConfig{Enabled: true, AppIDs: []string{nativeAPITestAppID}}}
	var testedUser string
	env.core.OnPushTestRequested = func(_ context.Context, userID string) error {
		testedUser = userID
		return nil
	}
	if _, err := env.push.SendTestNotification(ctx, connect.NewRequest(&apiv1.SendTestNotificationRequest{})); err != nil {
		t.Fatalf("SendTestNotification: %v", err)
	}
	if testedUser != env.viewer.Id {
		t.Fatalf("tested user = %q, want viewer", testedUser)
	}
}

func TestDeleteNativeDeviceByCapability(t *testing.T) {
	env := newConnectAPITestEnv(t)
	ctx := withCaller(env.ctx, env.viewer)
	env.api.config.Push = config.PushConfig{APNs: config.APNsConfig{Enabled: true, Topics: []string{nativeAPITestAppID}}}
	if _, err := env.push.RegisterNativeDevice(ctx, nativeRegisterRequest(apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS, nativeAPITestToken, nativeAPITestAppID)); err != nil {
		t.Fatalf("RegisterNativeDevice: %v", err)
	}
	if _, err := env.pushCleanup.DeleteNativeDevice(env.ctx, connect.NewRequest(&authv1.DeleteNativeDeviceRequest{
		Token:        nativeAPITestToken,
		CleanupToken: "wrong-cleanup-token-0123456789abcdefghij",
	})); err != nil {
		t.Fatalf("DeleteNativeDevice with a wrong token: %v", err)
	}
	if devices, _ := env.core.GetUserNativePushDevices(env.ctx, env.viewer.Id); len(devices) != 1 {
		t.Fatalf("devices after wrong capability = %d, want 1", len(devices))
	}
	if _, err := env.pushCleanup.DeleteNativeDevice(env.ctx, connect.NewRequest(&authv1.DeleteNativeDeviceRequest{
		Token:        nativeAPITestToken,
		CleanupToken: nativeAPITestCleanup,
	})); err != nil {
		t.Fatalf("DeleteNativeDevice: %v", err)
	}
	if devices, _ := env.core.GetUserNativePushDevices(env.ctx, env.viewer.Id); len(devices) != 0 {
		t.Fatalf("devices after capability cleanup = %d, want 0", len(devices))
	}
}
