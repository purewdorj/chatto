import { Capacitor, registerPlugin, type PluginListenerHandle } from '@capacitor/core';
import { NativePushPlatform } from '@chatto/api-types/api/v1/push_notifications_pb';

/**
 * Narrow adapter for the native push host capability (ADR-072). The mobile
 * shells bundle `@capacitor/push-notifications`; the browser has no such
 * plugin, so every function here reports "unavailable" there.
 *
 * The plugin is reached by name, so the frontend does not depend on the
 * plugin package. Only the methods and events that Chatto uses are typed.
 */

/** Permission state that the operating system reports for notifications. */
export type NativePushPermission = 'prompt' | 'prompt-with-rationale' | 'granted' | 'denied';

/** Custom data that the server puts in a native notification. */
export type NativePushData = Record<string, unknown>;

type PushNotificationsPlugin = {
  checkPermissions(): Promise<{ receive: NativePushPermission }>;
  requestPermissions(): Promise<{ receive: NativePushPermission }>;
  register(): Promise<void>;
  addListener(
    event: 'registration',
    listener: (token: { value: string }) => void
  ): Promise<PluginListenerHandle>;
  addListener(
    event: 'registrationError',
    listener: (error: { error: string }) => void
  ): Promise<PluginListenerHandle>;
  addListener(
    event: 'pushNotificationActionPerformed',
    listener: (action: { actionId: string; notification: { data?: NativePushData } }) => void
  ): Promise<PluginListenerHandle>;
};

const PLUGIN_NAME = 'PushNotifications';
const pushNotifications = registerPlugin<PushNotificationsPlugin>(PLUGIN_NAME);

/** How long a native registration may take before Chatto gives up. */
const REGISTRATION_TIMEOUT_MS = 30_000;

/**
 * The native push service of this host, or null when the host cannot receive
 * native push. iOS uses APNs and Android uses FCM.
 */
export function nativePushPlatform(): NativePushPlatform | null {
  if (!Capacitor.isPluginAvailable(PLUGIN_NAME)) return null;
  switch (Capacitor.getPlatform()) {
    case 'ios':
      return NativePushPlatform.APNS;
    case 'android':
      return NativePushPlatform.FCM;
    default:
      return null;
  }
}

/** Human-readable name of the host platform, for device labels. */
export function nativePlatformName(): string {
  return Capacitor.getPlatform() === 'ios' ? 'iOS' : 'Android';
}

/** The current notification permission. Never shows a prompt. */
export async function checkNativePushPermission(): Promise<NativePushPermission> {
  return (await pushNotifications.checkPermissions()).receive;
}

/** Asks the operating system for notification permission when it has not decided yet. */
export async function requestNativePushPermission(): Promise<NativePushPermission> {
  const current = await checkNativePushPermission();
  if (current === 'granted' || current === 'denied') return current;
  return (await pushNotifications.requestPermissions()).receive;
}

let tokenRequest: Promise<string> | null = null;

/**
 * Registers with APNs or FCM and returns the device token. Call only after
 * permission is granted. Concurrent callers share one registration. A failed
 * registration, for example an Android build without Firebase configuration,
 * rejects so that the next call tries again.
 */
export function nativePushToken(): Promise<string> {
  tokenRequest ??= requestToken().catch((error: unknown) => {
    tokenRequest = null;
    throw error;
  });
  return tokenRequest;
}

async function requestToken(): Promise<string> {
  const handles: PluginListenerHandle[] = [];
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await new Promise<string>((resolve, reject) => {
      timer = setTimeout(
        () => reject(new Error('Native push registration timed out')),
        REGISTRATION_TIMEOUT_MS
      );
      void Promise.all([
        pushNotifications.addListener('registration', (token) => resolve(token.value)),
        pushNotifications.addListener('registrationError', (failure) =>
          reject(new Error(failure.error || 'Native push registration failed'))
        )
      ])
        .then((added) => {
          handles.push(...added);
          return pushNotifications.register();
        })
        .catch(reject);
    });
  } finally {
    clearTimeout(timer);
    for (const handle of handles) void handle.remove();
  }
}

/**
 * Calls `listener` when the user opens a native notification. The plugin
 * keeps the notification that launched the app until the first listener is
 * added, so a listener added during startup also receives it.
 */
export async function onNativePushOpened(
  listener: (data: NativePushData) => void
): Promise<() => void> {
  const handle = await pushNotifications.addListener('pushNotificationActionPerformed', (action) =>
    listener(action.notification.data ?? {})
  );
  return () => void handle.remove();
}
