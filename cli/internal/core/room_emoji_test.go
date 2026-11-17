package core

import (
	"testing"

	"github.com/chattocorp/chatto/cli/internal/evtstream"
	evtv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/evt/v1"
	"github.com/stretchr/testify/require"
)

func TestRoomEmojiSetting(t *testing.T) {
	chatto, _ := setupTestCore(t)
	ctx := testContext(t)
	manager, err := chatto.CreateUser(ctx, SystemActorID, "emoji-manager", "Manager", "password123")
	require.NoError(t, err)
	room, err := chatto.CreateRoom(ctx, SystemActorID, KindChannel, "", "launch", "")
	require.NoError(t, err)
	require.Empty(t, room.Emoji)

	input := RoomUpdateInput{ActorID: manager.Id, RoomID: room.Id, Emoji: new("🚀")}
	_, err = chatto.RoomCommands().UpdateRoom(ctx, input)
	require.ErrorIs(t, err, ErrPermissionDenied)
	require.NoError(t, chatto.GrantUserRoomPermission(ctx, SystemActorID, room.Id, manager.Id, PermRoomManage))

	updated, err := chatto.RoomCommands().UpdateRoom(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "🚀", updated.Emoji)
	_, err = chatto.RoomCommands().UpdateRoom(ctx, input)
	require.NoError(t, err)
	facts, _, err := chatto.EventPublisher.SubjectEvents(ctx, evtstream.RoomAggregate(room.Id).Subject(evtstream.EventRoomEmojiChanged))
	require.NoError(t, err)
	require.Len(t, facts, 1, "an unchanged emoji must not append another fact")
	require.False(t, IsVisibleRoomTimelineEntry(facts[0]))
	require.True(t, isDeliverableLiveEVTRoomEvent(facts[0]))
	require.Equal(t, room.Id, roomIDOfEvent(facts[0]))

	for _, invalid := range []string{"rocket", "🚀🚀", ":rocket:", "a"} {
		input.Emoji = new(invalid)
		_, err = chatto.RoomCommands().UpdateRoom(ctx, input)
		require.ErrorIs(t, err, ErrInvalidArgument, invalid)
	}

	input.Emoji = new(" ")
	updated, err = chatto.RoomCommands().UpdateRoom(ctx, input)
	require.NoError(t, err)
	require.Empty(t, updated.Emoji, "an empty emoji clears the channel emoji")

	dm, _, err := chatto.FindOrCreateDM(ctx, manager.Id, []string{manager.Id})
	require.NoError(t, err)
	input.RoomID = dm.Id
	input.Emoji = new("🚀")
	_, err = chatto.RoomCommands().UpdateRoom(ctx, input)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestRoomEmojiReplayAndSnapshot(t *testing.T) {
	p := NewRoomDirectoryProjection()
	require.NoError(t, p.Apply(&evtv1.Event{Event: &evtv1.Event_RoomCreated{RoomCreated: &evtv1.RoomCreatedEvent{RoomId: "legacy", Kind: evtv1.RoomKind_ROOM_KIND_CHANNEL}}}, 1))
	room, ok := p.Catalog.Get("legacy")
	require.True(t, ok)
	require.Empty(t, room.Emoji, "old rooms have no emoji")
	for i, emoji := range []string{"🎉", ""} {
		require.NoError(t, p.Apply(&evtv1.Event{Event: &evtv1.Event_RoomEmojiChanged{RoomEmojiChanged: &evtv1.RoomEmojiChangedEvent{RoomId: "legacy", Emoji: emoji}}}, uint64(i+2)))
		payload, err := p.Snapshot()
		require.NoError(t, err)
		restored := NewRoomDirectoryProjection()
		require.NoError(t, restored.Restore(payload))
		room, ok := restored.Catalog.Get("legacy")
		require.True(t, ok)
		require.Equal(t, emoji, room.Emoji)
	}
}
