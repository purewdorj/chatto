package connectapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/chattocorp/chatto/cli/internal/config"
	"github.com/chattocorp/chatto/cli/internal/core"
	apiv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/api/v1"
	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

type pushNotificationService struct {
	api *API
}

func (s *pushNotificationService) SendTestNotification(ctx context.Context, _ *connect.Request[apiv1.SendTestNotificationRequest]) (*connect.Response[apiv1.SendTestNotificationResponse], error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if (!s.api.config.Push.IsConfigured() && !s.api.config.Push.NativeConfigured()) || s.api.core.OnPushTestRequested == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("push notifications are not enabled on this instance"))
	}
	if err := s.api.core.AdmitPushTestNotification(ctx, caller.UserID); err != nil {
		if errors.Is(err, core.ErrPushTestNotificationRateLimited) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("test push notification rate limit exceeded"))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("push notification could not be delivered"))
	}

	if err := s.api.core.OnPushTestRequested(ctx, caller.UserID); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("push notification could not be delivered"))
	}
	return connect.NewResponse(&apiv1.SendTestNotificationResponse{}), nil
}

func (s *pushNotificationService) Subscribe(ctx context.Context, req *connect.Request[apiv1.SubscribeRequest]) (*connect.Response[apiv1.SubscribeResponse], error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !s.api.config.Push.IsConfigured() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("push notifications are not enabled on this instance"))
	}

	userAgent := ""
	if req.Msg.UserAgent != nil {
		userAgent = req.Msg.GetUserAgent()
	}
	if req.Msg.GetClientHost() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client host is required"))
	}
	if req.Msg.GetCleanupToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cleanup token is required"))
	}

	if _, err := s.api.core.SavePushSubscriptionForClientWithCleanupToken(ctx, caller.UserID, req.Msg.GetEndpoint(), req.Msg.GetP256Dh(), req.Msg.GetAuth(), userAgent, req.Msg.GetClientHost(), req.Msg.GetCleanupToken()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&apiv1.SubscribeResponse{}), nil
}

func (s *pushNotificationService) Unsubscribe(ctx context.Context, req *connect.Request[apiv1.UnsubscribeRequest]) (*connect.Response[apiv1.UnsubscribeResponse], error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.api.core.DeletePushSubscription(ctx, caller.UserID, req.Msg.GetEndpoint()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&apiv1.UnsubscribeResponse{}), nil
}

func (s *pushNotificationService) RegisterNativeDevice(ctx context.Context, req *connect.Request[apiv1.RegisterNativeDeviceRequest]) (*connect.Response[apiv1.RegisterNativeDeviceResponse], error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	platform, ok := nativePushDevicePlatform(req.Msg.GetPlatform())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("native push platform is required"))
	}
	if !nativePushPlatformEnabled(s.api.config.Push, req.Msg.GetPlatform()) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("native push for this platform is not enabled on this instance"))
	}
	if !nativePushAppAllowed(s.api.config.Push, req.Msg.GetPlatform(), req.Msg.GetAppId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("app ID is not allowed on this instance"))
	}
	if req.Msg.GetCleanupToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cleanup token is required"))
	}
	if _, err := s.api.core.SaveNativePushDevice(ctx, caller.UserID, core.NativePushDeviceRegistration{
		Platform:     platform,
		Token:        req.Msg.GetToken(),
		AppID:        req.Msg.GetAppId(),
		APNsSandbox:  req.Msg.GetApnsSandbox(),
		DeviceName:   req.Msg.GetDeviceName(),
		CleanupToken: req.Msg.GetCleanupToken(),
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&apiv1.RegisterNativeDeviceResponse{}), nil
}

func (s *pushNotificationService) UnregisterNativeDevice(ctx context.Context, req *connect.Request[apiv1.UnregisterNativeDeviceRequest]) (*connect.Response[apiv1.UnregisterNativeDeviceResponse], error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.api.core.DeleteNativePushDevice(ctx, caller.UserID, req.Msg.GetToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&apiv1.UnregisterNativeDeviceResponse{}), nil
}

// nativePushDevicePlatform maps the public platform to the stored platform.
func nativePushDevicePlatform(platform apiv1.NativePushPlatform) (runtimestatev1.NativePushDevicePlatform, bool) {
	switch platform {
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS:
		return runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS, true
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP:
		return runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP, true
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_FCM:
		return runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM, true
	default:
		return runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_UNSPECIFIED, false
	}
}

// nativePushPlatformEnabled reports whether the operator enabled platform.
// Startup fails when an enabled provider cannot be used, so configuration
// and delivery agree.
func nativePushPlatformEnabled(cfg config.PushConfig, platform apiv1.NativePushPlatform) bool {
	switch platform {
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS, apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP:
		return cfg.APNs.Enabled
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_FCM:
		return cfg.FCM.Enabled
	default:
		return false
	}
}

// nativePushAppAllowed reports whether appID is in the operator allow-list
// of platform. APNs VoIP devices use the bundle ID of the app, so they share
// the APNs topic allow-list.
func nativePushAppAllowed(cfg config.PushConfig, platform apiv1.NativePushPlatform, appID string) bool {
	switch platform {
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS, apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP:
		return cfg.APNs.AllowsTopic(appID)
	case apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_FCM:
		return cfg.FCM.AllowsAppID(appID)
	default:
		return false
	}
}

// nativePushPlatforms returns the native platforms that the operator enabled,
// for ServerRuntimeConfig.native_push_platforms.
func nativePushPlatforms(cfg config.PushConfig) []apiv1.NativePushPlatform {
	var platforms []apiv1.NativePushPlatform
	if cfg.APNs.Enabled {
		platforms = append(platforms,
			apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS,
			apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_APNS_VOIP)
	}
	if cfg.FCM.Enabled {
		platforms = append(platforms, apiv1.NativePushPlatform_NATIVE_PUSH_PLATFORM_FCM)
	}
	return platforms
}
