import { Capacitor, registerPlugin } from '@capacitor/core';

/**
 * Narrow adapter for the native ringtone host capability (ADR-072). The
 * mobile shells ring with a system sound that needs no user interaction and
 * follows the device's ringer and silent settings. Browsers have no such
 * plugin and use the Web Audio ringtone.
 */
const nativeRingtone = registerPlugin<{
  start(): Promise<void>;
  stop(): Promise<void>;
}>('ChattoRingtone');

/** Whether the host can ring natively. */
export function hasNativeRingtone(): boolean {
  return Capacitor.isPluginAvailable('ChattoRingtone');
}

/**
 * Start the native ring and return its stop function. The ring repeats until
 * stopped. Failures are logged and leave the call banner as the only signal.
 */
export function startNativeRingtone(): () => void {
  let stopped = false;
  const started = nativeRingtone.start().catch((error: unknown) => {
    console.error('Failed to start the native ringtone:', error);
  });
  return () => {
    if (stopped) return;
    stopped = true;
    // Stop after the start settles, so a quick stop cannot leave it ringing.
    void started.then(() =>
      nativeRingtone.stop().catch((error: unknown) => {
        console.error('Failed to stop the native ringtone:', error);
      })
    );
  };
}
