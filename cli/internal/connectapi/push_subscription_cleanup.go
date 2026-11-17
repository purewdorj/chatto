package connectapi

import (
	"context"

	"connectrpc.com/connect"
	authv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/auth/v1"
)

type pushSubscriptionCleanupService struct {
	api *API
}

func (s *pushSubscriptionCleanupService) DeleteSubscription(ctx context.Context, req *connect.Request[authv1.DeleteSubscriptionRequest]) (*connect.Response[authv1.DeleteSubscriptionResponse], error) {
	if err := s.api.core.DeletePushSubscriptionByCapability(ctx, req.Msg.GetEndpoint(), req.Msg.GetAuth(), req.Msg.GetCleanupToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.DeleteSubscriptionResponse{}), nil
}

func (s *pushSubscriptionCleanupService) DeleteNativeDevice(ctx context.Context, req *connect.Request[authv1.DeleteNativeDeviceRequest]) (*connect.Response[authv1.DeleteNativeDeviceResponse], error) {
	if err := s.api.core.DeleteNativePushDeviceByCapability(ctx, req.Msg.GetToken(), req.Msg.GetCleanupToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.DeleteNativeDeviceResponse{}), nil
}
