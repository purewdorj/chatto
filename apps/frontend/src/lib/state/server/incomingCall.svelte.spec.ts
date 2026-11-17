import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { INCOMING_CALL_RING_MS, IncomingCallState } from './incomingCall.svelte';

const started = {
  roomId: 'dm-1',
  callId: 'call-1',
  callerId: 'alice',
  viewerId: 'bob'
};

describe('IncomingCallState', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('rings for a call that another member starts in an eligible room', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted(started);
    expect(state.ringing).toEqual({ roomId: 'dm-1', callId: 'call-1', callerId: 'alice' });
  });

  it('does not ring for a call that the viewer starts', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted({ ...started, callerId: 'bob' });
    expect(state.ringing).toBeNull();
  });

  it('does not ring in a room that is not eligible', () => {
    const shouldRing = vi.fn(() => false);
    const state = new IncomingCallState(shouldRing);
    state.handleCallStarted(started);
    expect(shouldRing).toHaveBeenCalledWith('dm-1');
    expect(state.ringing).toBeNull();
  });

  it('stops when the ring time passes', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted(started);
    vi.advanceTimersByTime(INCOMING_CALL_RING_MS - 1);
    expect(state.ringing).not.toBeNull();
    vi.advanceTimersByTime(1);
    expect(state.ringing).toBeNull();
  });

  it('stops when the call that rings ends, but not for a different call', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted(started);
    state.handleCallEnded('dm-1', 'call-0');
    state.handleCallEnded('dm-2', 'call-1');
    expect(state.ringing).not.toBeNull();
    state.handleCallEnded('dm-1', 'call-1');
    expect(state.ringing).toBeNull();
  });

  it('stops when the viewer joins or loses access to the room', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted(started);
    state.handleViewerJoined('dm-1');
    expect(state.ringing).toBeNull();

    state.handleCallStarted(started);
    state.handleRoomAccessLost('dm-1');
    expect(state.ringing).toBeNull();
  });

  it('replaces the ring with a newer call and restarts the ring time', () => {
    const state = new IncomingCallState(() => true);
    state.handleCallStarted(started);
    vi.advanceTimersByTime(INCOMING_CALL_RING_MS - 1_000);
    state.handleCallStarted({ ...started, roomId: 'dm-2', callId: 'call-2' });
    vi.advanceTimersByTime(1_000);
    expect(state.ringing?.roomId).toBe('dm-2');
    vi.advanceTimersByTime(INCOMING_CALL_RING_MS);
    expect(state.ringing).toBeNull();
  });
});
