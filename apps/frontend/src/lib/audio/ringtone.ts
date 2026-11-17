/**
 * Incoming-call ringtone, synthesized with Web Audio.
 *
 * The ringtone repeats a two-tone ring until the returned stop function is
 * called. Only one ringtone plays at a time: a new start stops the previous
 * one. Failed playback is silent and never throws.
 *
 * Browsers block page audio until the user interacts with the page after it
 * loads. A ring that starts before that interaction waits, and the first
 * pointer or key press while it rings makes it audible. Check
 * {@link canPlayRingtone} to choose a second signal, such as a system
 * notification, for a ring that the browser blocks.
 *
 * The mobile apps ring natively instead (`$lib/desktop/nativeRingtone`). A
 * native ring needs no interaction and follows the device's silent setting.
 */

import { hasNativeRingtone, startNativeRingtone } from '$lib/desktop/nativeRingtone';

/** Time from the start of one ring to the start of the next. */
export const RING_PERIOD_MS = 3_000;
/** Length of one ring burst, in seconds. */
const RING_SECONDS = 1.2;
/** Output level of the ring. The two tones together stay below clipping. */
const RING_GAIN = 0.18;
/** Tone pair of the ring, in Hz. */
const RING_FREQUENCIES = [440, 480] as const;

/** User interactions that let a blocked audio context start. */
const UNLOCK_EVENTS = ['pointerdown', 'keydown', 'touchend'] as const;

let audioCtx: AudioContext | null = null;
let stopCurrent: (() => void) | null = null;

function getContext(): AudioContext | null {
  if (typeof AudioContext === 'undefined') return null;
  try {
    if (!audioCtx || audioCtx.state === 'closed') audioCtx = new AudioContext();
    if (audioCtx.state === 'suspended') void audioCtx.resume().catch(() => {});
    return audioCtx;
  } catch {
    return null;
  }
}

/**
 * Whether the browser lets the page play the ringtone now. It is false before
 * the first user interaction after a page load, when the browser blocks audio.
 */
export function canPlayRingtone(): boolean {
  if (hasNativeRingtone()) return true;
  if (typeof navigator === 'undefined') return false;
  // Browsers without the User Activation API do not report the block.
  return navigator.userActivation?.hasBeenActive ?? true;
}

/**
 * Resume the context on the first user interaction, then call `onResumed`.
 * Returns the removal function.
 */
function resumeOnInteraction(ctx: AudioContext, onResumed: () => void): () => void {
  if (typeof window === 'undefined' || ctx.state === 'running') return () => {};
  const resume = () => {
    remove();
    void ctx.resume().then(onResumed, () => {});
  };
  const remove = () => {
    for (const type of UNLOCK_EVENTS) window.removeEventListener(type, resume, true);
  };
  for (const type of UNLOCK_EVENTS) window.addEventListener(type, resume, true);
  return remove;
}

/** Schedule one ring burst and return its nodes so a stop can silence them. */
function playRing(ctx: AudioContext): AudioScheduledSourceNode[] {
  const now = ctx.currentTime;
  const output = ctx.createGain();
  output.gain.setValueAtTime(0, now);
  // Two short pulses in one burst, like a classic phone ring.
  for (const offset of [0, RING_SECONDS / 2]) {
    const start = now + offset;
    const end = start + RING_SECONDS / 2 - 0.08;
    output.gain.setValueAtTime(0, start);
    output.gain.linearRampToValueAtTime(RING_GAIN, start + 0.02);
    output.gain.setValueAtTime(RING_GAIN, end - 0.04);
    output.gain.linearRampToValueAtTime(0, end);
  }
  output.connect(ctx.destination);
  const oscillators = RING_FREQUENCIES.map((frequency) => {
    const oscillator = ctx.createOscillator();
    oscillator.type = 'sine';
    oscillator.frequency.value = frequency;
    oscillator.connect(output);
    oscillator.start(now);
    oscillator.stop(now + RING_SECONDS);
    return oscillator;
  });
  oscillators[0].onended = () => output.disconnect();
  return oscillators;
}

/**
 * Start the ringtone. It repeats every {@link RING_PERIOD_MS} until the
 * returned function is called. Calling the returned function more than once
 * has no effect.
 */
export function startRingtone(): () => void {
  stopCurrent?.();
  if (hasNativeRingtone()) {
    const stopNative = startNativeRingtone();
    const stop = () => {
      if (stopCurrent !== stop) return;
      stopCurrent = null;
      stopNative();
    };
    stopCurrent = stop;
    return stop;
  }
  const ctx = getContext();
  if (!ctx) return () => {};

  let active: AudioScheduledSourceNode[] = [];
  let lastRingAt = -Infinity;
  // A suspended context does not advance its clock. Skip rings until it runs,
  // so a blocked ring does not queue bursts that play together later. Two
  // resume paths can both ring; the second one inside a period is skipped.
  const ring = () => {
    if (stopCurrent !== stop || ctx.state !== 'running') return;
    const now = Date.now();
    if (now - lastRingAt < RING_PERIOD_MS - 250) return;
    lastRingAt = now;
    try {
      active = playRing(ctx);
    } catch {
      active = [];
    }
  };
  const interval = setInterval(ring, RING_PERIOD_MS);
  const stopResumeOnInteraction = resumeOnInteraction(ctx, ring);

  const stop = () => {
    if (stopCurrent !== stop) return;
    stopCurrent = null;
    clearInterval(interval);
    stopResumeOnInteraction();
    for (const node of active) {
      try {
        node.stop();
      } catch {
        // The node already stopped.
      }
    }
    active = [];
  };
  stopCurrent = stop;
  if (ctx.state === 'running') ring();
  else void ctx.resume().then(ring, () => {});
  return stop;
}
