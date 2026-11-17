package core

import (
	"context"
	"errors"
	"fmt"
)

// IncomingCallAlertRecipients returns the accounts that an incoming-call push
// alert can ring for a call that starterID started. It matches the
// incoming-call banner of the bundled client: only a direct message rings,
// only its other members ring, and only while they can join the call, are not
// in it, and are not in Do Not Disturb. It returns no recipients when callID
// is no longer the active call of the room.
//
// Alerts are not bound to a session, so permissions use the unprivileged view.
func (c *ChattoCore) IncomingCallAlertRecipients(ctx context.Context, roomID, callID, starterID string) ([]string, error) {
	kind, err := c.FindRoomKind(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("resolve incoming-call room kind: %w", err)
	}
	if kind != KindDM {
		return nil, nil
	}
	snapshot := c.callModel.callState.Projection().RoomSnapshot(roomID)
	if snapshot.Call.CallID != callID {
		return nil, nil
	}
	inCall := make(map[string]bool, len(snapshot.Participants))
	for _, participant := range snapshot.Participants {
		inCall[participant.UserID] = true
	}
	members, err := c.GetRoomMembersList(ctx, KindDM, roomID)
	if err != nil {
		return nil, fmt.Errorf("list incoming-call room members: %w", err)
	}
	var recipients []string
	for _, member := range members {
		userID := member.GetUserId()
		if userID == "" || userID == starterID || inCall[userID] {
			continue
		}
		permissions, err := c.unprivilegedCallPermissions(ctx, userID, roomID)
		if err != nil {
			return nil, err
		}
		if !permissions.Join {
			continue
		}
		presence, err := c.GetUserPresence(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("read incoming-call recipient presence: %w", err)
		}
		if presence == PresenceStatusDoNotDisturb {
			continue
		}
		recipients = append(recipients, userID)
	}
	return recipients, nil
}

// IncomingCallStillRinging reports whether callID is still the active call of
// roomID and userID has not joined it. Push delivery checks it again
// immediately before it contacts a provider.
func (c *ChattoCore) IncomingCallStillRinging(roomID, callID, userID string) bool {
	snapshot := c.callModel.callState.Projection().RoomSnapshot(roomID)
	if snapshot.Call.CallID != callID {
		return false
	}
	for _, participant := range snapshot.Participants {
		if participant.UserID == userID {
			return false
		}
	}
	return true
}

func (c *ChattoCore) unprivilegedCallPermissions(ctx context.Context, userID, roomID string) (CallPermissions, error) {
	var permissions CallPermissions
	authCtx := withPrivilegedModeEvaluation(ctx, userID, false)
	err := c.ReadServerContentView(authCtx, func(readCtx context.Context, _ uint64) error {
		_, kind, err := c.requireRoomMember(readCtx, userID, roomID)
		if err != nil {
			return err
		}
		permissions, err = c.resolveCallPermissions(readCtx, userID, kind, roomID)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotRoomMember) || errors.Is(err, ErrNotFound) {
			return CallPermissions{}, nil
		}
		return CallPermissions{}, fmt.Errorf("resolve incoming-call permissions: %w", err)
	}
	return permissions, nil
}
