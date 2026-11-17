import { Capacitor, registerPlugin, type PluginListenerHandle } from '@capacitor/core';

/**
 * Narrow adapter for the native incoming-call capability of the mobile apps
 * (ADR-072). iOS rings through CallKit after a PushKit VoIP push; Android
 * shows a full-screen call notification after a high-priority FCM message.
 * Both ring while the app is closed or the phone is locked. The native UI
 * only rings: the call itself runs in the web app after the user answers.
 *
 * Browsers have no such plugin; every function here then does nothing.
 */

/** A call that the native UI rang, as the server described it in the push. */
export type NativeCallInfo = {
  callId: string;
  roomId: string;
  /** Origin of the server that sent the push. */
  serverOrigin: string;
  /** The server's notification URL for the conversation. */
  url: string;
  callerName: string;
};

type ChattoCallsPlugin = {
  getVoipToken(): Promise<{ token: string | null }>;
  getPendingAnswers(): Promise<{ calls: NativeCallInfo[] }>;
  endCall(options: { callId: string }): Promise<void>;
  addListener(
    event: 'voipToken',
    listener: (data: { token: string | null }) => void
  ): Promise<PluginListenerHandle>;
  addListener(
    event: 'callAnswered' | 'callDeclined',
    listener: (call: NativeCallInfo) => void
  ): Promise<PluginListenerHandle>;
};

const PLUGIN_NAME = 'ChattoCalls';
const nativeCalls = registerPlugin<ChattoCallsPlugin>(PLUGIN_NAME);

/** Whether the host rings calls natively. */
export function hasNativeCalls(): boolean {
  return Capacitor.isPluginAvailable(PLUGIN_NAME);
}

/**
 * Whether the native call UI also rings while the app is in front. iOS
 * CallKit always shows the call; Android shows it only in the background.
 */
export function nativeCallsRingInForeground(): boolean {
  return hasNativeCalls() && Capacitor.getPlatform() === 'ios';
}

/** The PushKit VoIP token on iOS, or null where the platform has none. */
export async function nativeVoipToken(): Promise<string | null> {
  if (!hasNativeCalls()) return null;
  try {
    return (await nativeCalls.getVoipToken()).token ?? null;
  } catch (error) {
    console.error('Failed to read the VoIP token:', error);
    return null;
  }
}

/** Calls `listener` when PushKit issues a new VoIP token or invalidates it (null). */
export async function onNativeVoipToken(
  listener: (token: string | null) => void
): Promise<() => void> {
  if (!hasNativeCalls()) return () => {};
  const handle = await nativeCalls.addListener('voipToken', ({ token }) => listener(token));
  return () => void handle.remove();
}

/**
 * Calls the listeners when the user answers or declines a native call. The
 * native side queues an answer while no listener exists, for example the one
 * that launched the app; those answers are delivered after the listeners are
 * added. Call {@link deliverPendingNativeAnswers} again when the app returns
 * to the foreground.
 */
export async function onNativeCallActions(listeners: {
  answered: (call: NativeCallInfo) => void;
  declined: (call: NativeCallInfo) => void;
}): Promise<() => void> {
  if (!hasNativeCalls()) return () => {};
  const handles = await Promise.all([
    nativeCalls.addListener('callAnswered', listeners.answered),
    nativeCalls.addListener('callDeclined', listeners.declined)
  ]);
  await deliverPendingNativeAnswers(listeners.answered);
  return () => {
    for (const handle of handles) void handle.remove();
  };
}

/** Delivers and clears the answers that the native side queued. */
export async function deliverPendingNativeAnswers(
  answered: (call: NativeCallInfo) => void
): Promise<void> {
  if (!hasNativeCalls()) return;
  try {
    const { calls } = await nativeCalls.getPendingAnswers();
    for (const call of calls ?? []) answered(call);
  } catch (error) {
    console.error('Failed to read answered calls:', error);
  }
}

/** Stops the native ring or call UI for a call. Does nothing for an unknown call. */
export function endNativeCall(callId: string | null): void {
  if (!callId || !hasNativeCalls()) return;
  nativeCalls.endCall({ callId }).catch((error: unknown) => {
    console.error('Failed to end the native call:', error);
  });
}
