/**
 * The incoming direct-message call of one server, which rings the viewer.
 *
 * A `voiceCallStarted` event from another member in an eligible room starts
 * the ring. The ring stops when the call ends, the viewer joins it, the
 * viewer declines it, {@link INCOMING_CALL_RING_MS} passes, or the store
 * resets. Only the most recent call rings; a newer call replaces it.
 *
 * The ring is local UI state. Declining it does not change the call for other
 * participants.
 */

/** How long an unanswered call rings. */
export const INCOMING_CALL_RING_MS = 30_000;

/** A call that rings the viewer. */
export type IncomingCall = {
  roomId: string;
  callId: string | null;
  /** The member who started the call, or null when the event has no actor. */
  callerId: string | null;
};

/** Facts of a `voiceCallStarted` event. */
export type CallStartedFacts = IncomingCall & {
  /** The current viewer, to skip calls that the viewer started. */
  viewerId: string | null;
};

export class IncomingCallState {
  /** The call that rings now, or null. */
  ringing = $state<IncomingCall | null>(null);

  readonly #shouldRing: (roomId: string) => boolean;
  #timeout: ReturnType<typeof setTimeout> | null = null;

  /**
   * @param shouldRing Whether a call in this room may ring the viewer, for
   *   example only in a direct message that the viewer can join, while the
   *   viewer is not in a call.
   */
  constructor(shouldRing: (roomId: string) => boolean) {
    this.#shouldRing = shouldRing;
  }

  /** Start to ring for a call that another member started. */
  handleCallStarted(facts: CallStartedFacts): void {
    if (facts.callerId && facts.callerId === facts.viewerId) return;
    if (!this.#shouldRing(facts.roomId)) return;
    this.#clearTimeout();
    this.ringing = { roomId: facts.roomId, callId: facts.callId, callerId: facts.callerId };
    this.#timeout = setTimeout(() => this.clear(), INCOMING_CALL_RING_MS);
  }

  /** Stop the ring when the viewer joins the call that rings. */
  handleViewerJoined(roomId: string): void {
    if (this.ringing?.roomId === roomId) this.clear();
  }

  /** Stop the ring when the call that rings ends. */
  handleCallEnded(roomId: string, callId: string | null): void {
    const ringing = this.ringing;
    if (!ringing || ringing.roomId !== roomId) return;
    if (callId && ringing.callId && callId !== ringing.callId) return;
    this.clear();
  }

  /** Stop the ring in a room that the viewer can no longer access. */
  handleRoomAccessLost(roomId: string): void {
    if (this.ringing?.roomId === roomId) this.clear();
  }

  /** Stop the ring without a change to the call. */
  clear(): void {
    this.#clearTimeout();
    this.ringing = null;
  }

  #clearTimeout(): void {
    if (this.#timeout) clearTimeout(this.#timeout);
    this.#timeout = null;
  }
}
