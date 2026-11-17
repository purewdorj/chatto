package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type startedCall struct {
	roomID, callID, starterID string
}

func TestIncomingCallAlertRecipients(t *testing.T) {
	c, _ := setupTestCore(t)
	ctx := testContext(t)
	started := make(chan startedCall, 4)
	c.SetCallStartedHandler(func(_ context.Context, roomID, callID, starterID string) {
		started <- startedCall{roomID: roomID, callID: callID, starterID: starterID}
	})
	t.Cleanup(func() { c.SetCallStartedHandler(nil) })

	alice, err := c.CreateUser(ctx, SystemActorID, "call-alert-alice", "Alice", "password")
	require.NoError(t, err)
	bob, err := c.CreateUser(ctx, SystemActorID, "call-alert-bob", "Bob", "password")
	require.NoError(t, err)
	dm, _, err := c.roomCommands.StartDM(ctx, RoomStartDMInput{ActorID: alice.Id, ParticipantIDs: []string{bob.Id}})
	require.NoError(t, err)

	require.NoError(t, c.JoinVoiceCall(ctx, alice.Id, dm.Id))
	var call startedCall
	select {
	case call = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("call-started handler did not run")
	}
	require.Equal(t, dm.Id, call.roomID)
	require.Equal(t, alice.Id, call.starterID)
	require.NotEmpty(t, call.callID)

	recipients, err := c.IncomingCallAlertRecipients(ctx, dm.Id, call.callID, alice.Id)
	require.NoError(t, err)
	require.Equal(t, []string{bob.Id}, recipients)
	require.True(t, c.IncomingCallStillRinging(dm.Id, call.callID, bob.Id))

	// A stale call ID rings nobody.
	recipients, err = c.IncomingCallAlertRecipients(ctx, dm.Id, "other-call", alice.Id)
	require.NoError(t, err)
	require.Empty(t, recipients)

	// Do Not Disturb suppresses the ring.
	require.NoError(t, c.SetPresence(ctx, bob.Id, PresenceStatusDoNotDisturb))
	recipients, err = c.IncomingCallAlertRecipients(ctx, dm.Id, call.callID, alice.Id)
	require.NoError(t, err)
	require.Empty(t, recipients)
	require.NoError(t, c.SetPresence(ctx, bob.Id, PresenceStatusOnline))

	// A recipient who joins the call no longer rings, and a join does not start
	// a new call.
	require.NoError(t, c.JoinVoiceCall(ctx, bob.Id, dm.Id))
	require.False(t, c.IncomingCallStillRinging(dm.Id, call.callID, bob.Id))
	recipients, err = c.IncomingCallAlertRecipients(ctx, dm.Id, call.callID, alice.Id)
	require.NoError(t, err)
	require.Empty(t, recipients)
	select {
	case extra := <-started:
		t.Fatalf("join of a running call started another call: %+v", extra)
	default:
	}
}

func TestIncomingCallAlertRecipientsIgnoresChannels(t *testing.T) {
	c, _ := setupTestCore(t)
	ctx := testContext(t)
	alice, err := c.CreateUser(ctx, SystemActorID, "call-alert-channel-alice", "Alice", "password")
	require.NoError(t, err)
	bob, err := c.CreateUser(ctx, SystemActorID, "call-alert-channel-bob", "Bob", "password")
	require.NoError(t, err)
	room, err := c.CreateRoom(ctx, SystemActorID, KindChannel, "", "call-alert-channel", "")
	require.NoError(t, err)
	_, err = c.AddMember(ctx, SystemActorID, KindChannel, room.Id, alice.Id)
	require.NoError(t, err)
	_, err = c.AddMember(ctx, SystemActorID, KindChannel, room.Id, bob.Id)
	require.NoError(t, err)
	require.NoError(t, c.JoinVoiceCall(ctx, alice.Id, room.Id))
	snapshot, err := c.GetCallSnapshot(room.Id)
	require.NoError(t, err)

	recipients, err := c.IncomingCallAlertRecipients(ctx, room.Id, snapshot.Call.CallID, alice.Id)
	require.NoError(t, err)
	require.Empty(t, recipients)
}
