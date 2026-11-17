package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
	"github.com/chattocorp/chatto/pkg/jetstreamutil"
)

const (
	// callRingKeyPrefix is the RUNTIME_STATE key prefix of call ring records.
	// Format: call_ring.{callId}.{recipientUserId}
	callRingKeyPrefix = "call_ring."
	// CallRingTTL is how long a call ring record stays. A device rings for at
	// most 30 seconds after the push. The extra time covers a late delivery,
	// so a call end can still stop a ring that started late.
	CallRingTTL = 60 * time.Second
)

// callRingKey returns the KV key of the ring of recipientID for callID.
func callRingKey(callID, recipientID string) string {
	return callRingKeyPrefix + callID + "." + recipientID
}

// callRingKeyFilter returns the KV filter for all rings of callID.
func callRingKeyFilter(callID string) string {
	return callRingKeyPrefix + callID + ".*"
}

// CallRingStoppedHandler receives the accounts whose incoming-call ring must
// stop, because the call ended or because the account joined the call. The
// rings were recorded with RecordCallRing, and each ring reaches at most one
// handler run in the cluster. The handler runs in its own goroutine with a
// bounded context.
type CallRingStoppedHandler func(ctx context.Context, roomID, callID string, recipientIDs []string)

// RecordCallRing records that the server rings recipientID for callID in
// roomID. Call it before the incoming-call push, so a call end or a join that
// happens during the push still finds the ring. The record expires after
// CallRingTTL. A second record for the same ring is a no-op.
func (c *ChattoCore) RecordCallRing(ctx context.Context, roomID, callID, recipientID string) error {
	if roomID == "" || callID == "" || recipientID == "" {
		return fmt.Errorf("%w: room, call, and recipient are required", ErrInvalidArgument)
	}
	data, err := proto.Marshal(&runtimestatev1.CallRing{
		RoomId: roomID,
		RungAt: timestamppb.New(c.pushNow()),
	})
	if err != nil {
		return fmt.Errorf("marshal call ring: %w", err)
	}
	_, err = c.storage.runtimeStateKV.Create(ctx, callRingKey(callID, recipientID), data, jetstream.KeyTTL(CallRingTTL))
	if err != nil && !jetstreamutil.IsSequenceConflict(err) && !errors.Is(err, jetstream.ErrKeyExists) {
		return fmt.Errorf("store call ring: %w", err)
	}
	return nil
}

// claimCallRing removes the ring record of recipientID for callID. It returns
// true only to the caller whose revision-checked delete removed the record,
// so one replica sends the call-end push.
func (c *ChattoCore) claimCallRing(ctx context.Context, callID, recipientID string) (bool, error) {
	key := callRingKey(callID, recipientID)
	entry, err := c.storage.runtimeStateKV.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get call ring: %w", err)
	}
	err = c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(entry.Revision()))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, jetstream.ErrKeyNotFound) || jetstreamutil.IsSequenceConflict(err) {
		return false, nil
	}
	return false, fmt.Errorf("claim call ring: %w", err)
}

// ReleaseCallRing removes the ring record of recipientID for callID without a
// call-end push. The incoming-call handler calls it when the call stopped
// ringing before the push, so nothing must stop. Failures are logged only:
// the record expires after CallRingTTL.
func (c *ChattoCore) ReleaseCallRing(ctx context.Context, callID, recipientID string) {
	if _, err := c.claimCallRing(ctx, callID, recipientID); err != nil {
		c.logger.Warn("Failed to release call ring", "call_id", callID, "user_id", recipientID, "error", err)
	}
}

// claimCallRings claims the rings that a committed transition stops: the ring
// of joinedUserID when an account joined, or every ring of callID when the
// call ended.
func (c *ChattoCore) claimCallRings(ctx context.Context, callID, joinedUserID string, ended bool) ([]string, error) {
	if !ended {
		claimed, err := c.claimCallRing(ctx, callID, joinedUserID)
		if err != nil || !claimed {
			return nil, err
		}
		return []string{joinedUserID}, nil
	}
	keys, err := listPushRuntimeStateKeys(ctx, c.storage.runtimeStateKV, callRingKeyFilter(callID))
	if err != nil {
		return nil, fmt.Errorf("list call rings: %w", err)
	}
	prefix := callRingKeyPrefix + callID + "."
	var recipients []string
	var claimErrors []error
	for _, key := range keys {
		if len(key) <= len(prefix) {
			continue
		}
		recipientID := key[len(prefix):]
		claimed, err := c.claimCallRing(ctx, callID, recipientID)
		if err != nil {
			claimErrors = append(claimErrors, err)
			continue
		}
		if claimed {
			recipients = append(recipients, recipientID)
		}
	}
	return recipients, errors.Join(claimErrors...)
}

// SetCallRingStoppedHandler installs the handler that receives the rings that
// stop when a call ends or when a rung account joins the call. The replica
// that commits the transition claims the rings. Set it before Run. A nil
// handler removes it.
func (c *ChattoCore) SetCallRingStoppedHandler(handler CallRingStoppedHandler) {
	if handler == nil {
		c.callModel.callStarted.ringStop.Store(nil)
		return
	}
	hook := callRingStopHook(func(ctx context.Context, roomID, callID, joinedUserID string, ended bool) {
		if callID == "" || (!ended && joinedUserID == "") {
			return
		}
		recipients, err := c.claimCallRings(ctx, callID, joinedUserID, ended)
		if err != nil {
			c.logger.Warn("Failed to claim call rings", "room_id", roomID, "call_id", callID, "error", err)
		}
		if len(recipients) == 0 {
			return
		}
		handler(ctx, roomID, callID, recipients)
	})
	c.callModel.callStarted.ringStop.Store(&hook)
}
