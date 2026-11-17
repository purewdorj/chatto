package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	evtv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/evt/v1"
)

type stoppedRing struct {
	roomID, callID string
	recipientIDs   []string
}

func waitStoppedRing(t *testing.T, stopped <-chan stoppedRing) stoppedRing {
	t.Helper()
	select {
	case ring := <-stopped:
		return ring
	case <-time.After(5 * time.Second):
		t.Fatal("call-ring-stopped handler did not run")
		return stoppedRing{}
	}
}

func requireNoStoppedRing(t *testing.T, stopped <-chan stoppedRing) {
	t.Helper()
	select {
	case ring := <-stopped:
		t.Fatalf("unexpected call-ring-stopped run: %+v", ring)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCallRingStoppedByJoinAndByCallEnd(t *testing.T) {
	c, _ := setupTestCore(t)
	ctx := testContext(t)
	stopped := make(chan stoppedRing, 4)
	c.SetCallRingStoppedHandler(func(_ context.Context, roomID, callID string, recipientIDs []string) {
		stopped <- stoppedRing{roomID: roomID, callID: callID, recipientIDs: recipientIDs}
	})
	t.Cleanup(func() { c.SetCallRingStoppedHandler(nil) })

	alice, err := c.CreateUser(ctx, SystemActorID, "call-ring-alice", "Alice", "password")
	require.NoError(t, err)
	bob, err := c.CreateUser(ctx, SystemActorID, "call-ring-bob", "Bob", "password")
	require.NoError(t, err)
	dm, _, err := c.roomCommands.StartDM(ctx, RoomStartDMInput{ActorID: alice.Id, ParticipantIDs: []string{bob.Id}})
	require.NoError(t, err)

	// The recipient joins: only that recipient's ring stops.
	require.NoError(t, c.JoinVoiceCall(ctx, alice.Id, dm.Id))
	snapshot, err := c.GetCallSnapshot(dm.Id)
	require.NoError(t, err)
	firstCall := snapshot.Call.CallID
	require.NoError(t, c.RecordCallRing(ctx, dm.Id, firstCall, bob.Id))
	require.NoError(t, c.RecordCallRing(ctx, dm.Id, firstCall, bob.Id), "a repeated record is a no-op")
	require.NoError(t, c.JoinVoiceCall(ctx, bob.Id, dm.Id))
	ring := waitStoppedRing(t, stopped)
	require.Equal(t, stoppedRing{roomID: dm.Id, callID: firstCall, recipientIDs: []string{bob.Id}}, ring)

	// The ring was claimed, so the call end stops nothing more.
	require.NoError(t, c.callModel.AppendLeft(ctx, dm.Id, bob.Id, evtv1.CallParticipantEventSource_CALL_PARTICIPANT_EVENT_SOURCE_USER))
	require.NoError(t, c.callModel.AppendLeft(ctx, dm.Id, alice.Id, evtv1.CallParticipantEventSource_CALL_PARTICIPANT_EVENT_SOURCE_USER))
	requireNoStoppedRing(t, stopped)

	// The caller hangs up before the recipient answers: the call ends and
	// every ring of the call stops.
	require.NoError(t, c.JoinVoiceCall(ctx, alice.Id, dm.Id))
	snapshot, err = c.GetCallSnapshot(dm.Id)
	require.NoError(t, err)
	secondCall := snapshot.Call.CallID
	require.NotEqual(t, firstCall, secondCall)
	require.NoError(t, c.RecordCallRing(ctx, dm.Id, secondCall, bob.Id))
	require.NoError(t, c.callModel.AppendLeft(ctx, dm.Id, alice.Id, evtv1.CallParticipantEventSource_CALL_PARTICIPANT_EVENT_SOURCE_USER))
	ring = waitStoppedRing(t, stopped)
	require.Equal(t, stoppedRing{roomID: dm.Id, callID: secondCall, recipientIDs: []string{bob.Id}}, ring)

	// Each ring is claimed once.
	recipients, err := c.claimCallRings(ctx, secondCall, "", true)
	require.NoError(t, err)
	require.Empty(t, recipients)
}

func TestReleaseCallRingSendsNoCallEnd(t *testing.T) {
	c, _ := setupTestCore(t)
	ctx := testContext(t)
	stopped := make(chan stoppedRing, 2)
	c.SetCallRingStoppedHandler(func(_ context.Context, roomID, callID string, recipientIDs []string) {
		stopped <- stoppedRing{roomID: roomID, callID: callID, recipientIDs: recipientIDs}
	})
	t.Cleanup(func() { c.SetCallRingStoppedHandler(nil) })

	alice, err := c.CreateUser(ctx, SystemActorID, "call-ring-release-alice", "Alice", "password")
	require.NoError(t, err)
	bob, err := c.CreateUser(ctx, SystemActorID, "call-ring-release-bob", "Bob", "password")
	require.NoError(t, err)
	dm, _, err := c.roomCommands.StartDM(ctx, RoomStartDMInput{ActorID: alice.Id, ParticipantIDs: []string{bob.Id}})
	require.NoError(t, err)
	require.NoError(t, c.JoinVoiceCall(ctx, alice.Id, dm.Id))
	snapshot, err := c.GetCallSnapshot(dm.Id)
	require.NoError(t, err)

	require.NoError(t, c.RecordCallRing(ctx, dm.Id, snapshot.Call.CallID, bob.Id))
	c.ReleaseCallRing(ctx, snapshot.Call.CallID, bob.Id)
	require.NoError(t, c.callModel.AppendLeft(ctx, dm.Id, alice.Id, evtv1.CallParticipantEventSource_CALL_PARTICIPANT_EVENT_SOURCE_USER))
	requireNoStoppedRing(t, stopped)
}
