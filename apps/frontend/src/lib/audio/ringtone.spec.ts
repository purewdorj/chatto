import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

/** A Web Audio context that starts suspended, as before a user interaction. */
class FakeAudioContext {
  static instances: FakeAudioContext[] = [];
  state: AudioContextState = 'suspended';
  currentTime = 0;
  destination = {};
  oscillators: { start: ReturnType<typeof vi.fn>; stop: ReturnType<typeof vi.fn> }[] = [];
  allowResume = false;
  constructor() {
    FakeAudioContext.instances.push(this);
  }
  resume = vi.fn(() => {
    if (!this.allowResume) return new Promise<void>(() => {});
    this.state = 'running';
    return Promise.resolve();
  });
  createGain() {
    const param = { setValueAtTime: vi.fn(), linearRampToValueAtTime: vi.fn() };
    return { gain: param, connect: vi.fn(), disconnect: vi.fn() };
  }
  createOscillator() {
    const oscillator = {
      type: 'sine',
      frequency: { value: 0 },
      connect: vi.fn(),
      start: vi.fn(),
      stop: vi.fn(),
      onended: null
    };
    this.oscillators.push(oscillator);
    return oscillator;
  }
}

/** Ring bursts started so far; each burst starts two oscillators. */
function bursts(ctx: FakeAudioContext): number {
  return ctx.oscillators.filter((o) => o.start.mock.calls.length > 0).length / 2;
}

describe('ringtone', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeAudioContext.instances = [];
    vi.stubGlobal('AudioContext', FakeAudioContext);
    vi.stubGlobal('window', new EventTarget());
    // The module keeps one shared context; each test starts with a new one.
    vi.resetModules();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('waits while the browser blocks audio and rings once at the first interaction', async () => {
    const { RING_PERIOD_MS, startRingtone } = await import('./ringtone');
    const stop = startRingtone();
    const ctx = FakeAudioContext.instances.at(-1)!;
    await vi.advanceTimersByTimeAsync(RING_PERIOD_MS * 3);
    expect(bursts(ctx)).toBe(0);

    ctx.allowResume = true;
    window.dispatchEvent(new Event('pointerdown'));
    await vi.advanceTimersByTimeAsync(0);
    expect(bursts(ctx)).toBe(1);

    await vi.advanceTimersByTimeAsync(RING_PERIOD_MS);
    expect(bursts(ctx)).toBe(2);
    stop();
  });

  it('rings at once and repeats when audio is allowed, until stopped', async () => {
    const { RING_PERIOD_MS, startRingtone } = await import('./ringtone');
    const stop = startRingtone();
    const ctx = FakeAudioContext.instances.at(-1)!;
    ctx.state = 'running';
    stop();
    const restart = startRingtone();
    await vi.advanceTimersByTimeAsync(0);
    expect(bursts(ctx)).toBe(1);
    await vi.advanceTimersByTimeAsync(RING_PERIOD_MS);
    expect(bursts(ctx)).toBe(2);

    restart();
    await vi.advanceTimersByTimeAsync(RING_PERIOD_MS * 2);
    expect(bursts(ctx)).toBe(2);
    expect(ctx.oscillators.every((o) => o.stop.mock.calls.length > 0)).toBe(true);
  });
});
