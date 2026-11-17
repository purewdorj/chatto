/**
 * Native push notifications for the mobile apps.
 *
 * The iOS and Android shells receive notifications through APNs and FCM
 * instead of browser Web Push. This module registers the device token with
 * every signed-in server that can deliver to the platform, keeps the
 * registrations fresh, removes them when the user turns push off or leaves a
 * server, and opens the conversation when the user taps a notification.
 *
 * Push is on for every server or for none, as with Web Push. The device-wide
 * opt-out and nothing else is kept in local storage. Device tokens stay in
 * memory; the plugin returns the current token on each start.
 */

import { goto } from '$app/navigation';
import { resolve } from '$app/paths';
import { SvelteMap } from 'svelte/reactivity';
import { NativePushPlatform } from '@chatto/api-types/api/v1/push_notifications_pb';
import { createPushNotificationAPI } from '$lib/api/pushNotifications';
import { serverConnectionManager, serverRegistry } from '$lib/client';
import {
  checkNativePushPermission,
  nativePlatformName,
  nativePushPlatform,
  nativePushToken,
  onNativePushOpened,
  requestNativePushPermission,
  type NativePushData,
  type NativePushPermission
} from '$lib/desktop/nativePush';
import { nativeVoipToken, onNativeVoipToken } from '$lib/desktop/nativeCalls';
import { findServerByUrl } from '$lib/serverCatalogue';
import { serverIdToSegment } from '$lib/navigation';
import { errorMessage } from '$lib/utils/errorMessage';
import { conversationPath } from '$lib/navigation/conversationPath';

/** A server's registration is saved again after this time. The server expires it after 180 days. */
export const NATIVE_PUSH_REFRESH_INTERVAL_MS = 24 * 60 * 60 * 1000;

const OPT_OUT_KEY = 'chatto:native-push-disabled';

/** Identity of this app that the native shell injects before the page loads. */
type NativeAppIdentity = { appId: string; apnsSandbox: boolean };

/**
 * A save on one server. `voipToken` is the iOS PushKit token saved with it,
 * or null when the platform or the server has no VoIP calls.
 */
type SavedRegistration = {
  userId: string;
  token: string;
  voipToken: string | null;
  savedAt: number;
};

/** A signed-in server that can deliver native push to this platform. */
export type NativePushTarget = { serverId: string; userId: string };

function appIdentity(): NativeAppIdentity | null {
  const identity = (globalThis as { chattoNativeApp?: Partial<NativeAppIdentity> }).chattoNativeApp;
  if (typeof identity?.appId !== 'string' || identity.appId === '') return null;
  return { appId: identity.appId, apnsSandbox: identity.apnsSandbox === true };
}

function readOptOut(): boolean {
  try {
    return localStorage.getItem(OPT_OUT_KEY) === 'true';
  } catch {
    return false;
  }
}

function writeOptOut(disabled: boolean): void {
  try {
    if (disabled) localStorage.setItem(OPT_OUT_KEY, 'true');
    else localStorage.removeItem(OPT_OUT_KEY);
  } catch {
    // Storage can be unavailable. The choice then applies until the app restarts.
  }
}

function createCleanupToken(): string {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function pushAPI(serverId: string) {
  return serverConnectionManager.getClient(serverId).getAPI(createPushNotificationAPI);
}

/** The single owner of native push state in this app. */
class NativePushState {
  /** The native push service of this host, or null in a browser. */
  readonly platform: NativePushPlatform | null = nativePushPlatform();

  /** The operating system's notification permission, or null before the first check. */
  permission = $state<NativePushPermission | null>(null);
  /** Whether the user turned push off in this app. */
  disabled = $state(readOptOut());

  /** Successful saves in this app session, by server. */
  readonly #saved = new SvelteMap<string, SavedRegistration>();
  /** Technical reason of the latest failed save, by server. */
  readonly #failed = new SvelteMap<string, string>();
  readonly #inFlight = new Set<string>();
  #token: string | null = null;
  #voipToken: string | null = null;
  #voipListener: Promise<() => void> | null = null;
  #openListener: Promise<() => void> | null = null;

  /** Whether this host can receive native push at all. */
  get available(): boolean {
    return this.platform !== null && appIdentity() !== null;
  }

  /** Whether push is on in this app: permission granted and not turned off. */
  get on(): boolean {
    return this.permission === 'granted' && !this.disabled;
  }

  /** Short label for this device in settings. */
  get deviceName(): string {
    return nativePlatformName();
  }

  /** Signed-in servers that accept this platform's registrations. Reactive. */
  targets(): NativePushTarget[] {
    const platform = this.platform;
    if (platform === null) return [];
    return serverRegistry.servers.flatMap((server) => {
      const store = serverRegistry.tryGetStore(server.id);
      const userId = store?.accountId;
      if (!store?.isAuthenticated || !userId) return [];
      if (!store.serverInfo.nativePushPlatforms.includes(platform)) return [];
      return [{ serverId: server.id, userId }];
    });
  }

  /** Whether a server stores this device's current registration for the account. */
  isRegistered(serverId: string, userId: string | null): boolean {
    const saved = this.#saved.get(serverId);
    return !!saved && saved.userId === userId && saved.token === this.#token;
  }

  /**
   * Whether a server stores this device's current VoIP token, so that iOS
   * CallKit rings its calls also while the app is closed.
   */
  isVoipRegistered(serverId: string): boolean {
    const saved = this.#saved.get(serverId);
    return !!saved?.voipToken && saved.voipToken === this.#voipToken;
  }

  /** Technical reason of the latest failed save for a server, if any. */
  failure(serverId: string): string | null {
    return this.#failed.get(serverId) ?? null;
  }

  /** Reads the permission again, for example when the app returns to the foreground. */
  async refreshPermission(): Promise<NativePushPermission | null> {
    if (!this.available) return null;
    try {
      this.permission = await checkNativePushPermission();
    } catch (error) {
      console.error('Failed to read notification permission:', error);
    }
    return this.permission;
  }

  /**
   * Turns push on: clears the opt-out, asks for permission when the system
   * has not decided yet, and registers with every eligible server.
   */
  async enable(): Promise<NativePushPermission | null> {
    if (!this.available) return null;
    this.disabled = false;
    writeOptOut(false);
    this.permission = await requestNativePushPermission();
    if (this.permission === 'granted') await this.refresh({ force: true });
    return this.permission;
  }

  /** Turns push off and removes this device from every signed-in server. */
  async disable(): Promise<void> {
    this.disabled = true;
    writeOptOut(true);
    const results = await Promise.all(
      [...this.#saved.keys()].map((serverId) => this.#unregister(serverId))
    );
    if (!results.every(Boolean)) {
      throw new Error('Push notifications could not be turned off for every server');
    }
  }

  /**
   * Saves the registration of every eligible server whose save is missing or
   * out of date. Never asks for permission.
   */
  async refresh(options: { force?: boolean } = {}): Promise<void> {
    if (!this.available || this.disabled) return;
    if (this.permission === null) await this.refreshPermission();
    if (this.permission !== 'granted') return;

    let token: string;
    try {
      token = await nativePushToken();
    } catch (error) {
      const reason = errorMessage(error);
      for (const target of this.targets()) this.#failed.set(target.serverId, reason);
      console.error('Native push registration failed:', error);
      return;
    }
    this.#token = token;
    const voipToken = this.platform === NativePushPlatform.APNS ? await nativeVoipToken() : null;
    this.#voipToken = voipToken;

    const now = Date.now();
    await Promise.all(
      this.targets().map(async (target) => {
        const saved = this.#saved.get(target.serverId);
        const current =
          saved &&
          saved.userId === target.userId &&
          saved.token === token &&
          saved.voipToken === this.#voipTokenFor(target.serverId, voipToken) &&
          now - saved.savedAt < NATIVE_PUSH_REFRESH_INTERVAL_MS;
        if ((current && !options.force) || this.#inFlight.has(target.serverId)) return;
        await this.#register(target, token, voipToken);
      })
    );
  }

  /** Saves again at once after a failure. */
  async retry(serverId: string): Promise<void> {
    this.#failed.delete(serverId);
    await this.refresh({ force: true });
  }

  /** Sends a test notification through a server to this account's devices. */
  sendTest(serverId: string): Promise<boolean> {
    return pushAPI(serverId).sendTestNotification();
  }

  /** Removes this device from a server before the app signs out or forgets it. */
  async unsubscribeBeforeLeaving(serverId: string): Promise<void> {
    if (!this.#saved.has(serverId)) return;
    if (!(await this.#unregister(serverId))) {
      throw new Error('Push delivery could not be disabled before leaving the server');
    }
  }

  /**
   * Opens the conversation of each notification that the user taps. Call
   * once at startup; later calls do nothing.
   */
  listenForOpenedNotifications(): void {
    if (!this.available || this.#openListener) return;
    this.#openListener = onNativePushOpened((data) => void openNotification(data));
    this.#openListener.catch((error: unknown) => {
      this.#openListener = null;
      console.error('Failed to listen for opened notifications:', error);
    });
  }

  /**
   * Saves the registrations again when iOS issues a new VoIP token. Call once
   * at startup; later calls do nothing.
   */
  listenForVoipToken(): void {
    if (!this.available || this.#voipListener) return;
    this.#voipListener = onNativeVoipToken(() => void this.refresh({ force: true }));
    this.#voipListener.catch((error: unknown) => {
      this.#voipListener = null;
      console.error('Failed to listen for VoIP tokens:', error);
    });
  }

  /** The VoIP token that a server should store: none when it cannot ring through VoIP. */
  #voipTokenFor(serverId: string, voipToken: string | null): string | null {
    if (!voipToken) return null;
    const store = serverRegistry.tryGetStore(serverId);
    return store?.serverInfo.nativePushPlatforms.includes(NativePushPlatform.APNS_VOIP)
      ? voipToken
      : null;
  }

  async #register(
    target: NativePushTarget,
    token: string,
    availableVoipToken: string | null
  ): Promise<void> {
    const identity = appIdentity();
    const platform = this.platform;
    if (!identity || platform === null) return;
    this.#inFlight.add(target.serverId);
    try {
      const api = pushAPI(target.serverId);
      const device = {
        appId: identity.appId,
        apnsSandbox: platform === NativePushPlatform.APNS && identity.apnsSandbox,
        deviceName: this.deviceName
      };
      await api.registerNativeDevice({
        ...device,
        platform,
        token,
        cleanupToken: createCleanupToken()
      });
      const voipToken = this.#voipTokenFor(target.serverId, availableVoipToken);
      if (voipToken) {
        // PushKit uses the same APNs environment as the alert token.
        await api.registerNativeDevice({
          ...device,
          platform: NativePushPlatform.APNS_VOIP,
          token: voipToken,
          cleanupToken: createCleanupToken()
        });
      }
      this.#saved.set(target.serverId, {
        userId: target.userId,
        token,
        voipToken,
        savedAt: Date.now()
      });
      this.#failed.delete(target.serverId);
    } catch (error) {
      this.#failed.set(target.serverId, errorMessage(error));
      console.error('Failed to register this device for push notifications:', error);
    } finally {
      this.#inFlight.delete(target.serverId);
    }
  }

  async #unregister(serverId: string): Promise<boolean> {
    const saved = this.#saved.get(serverId);
    this.#saved.delete(serverId);
    this.#failed.delete(serverId);
    if (!saved) return true;
    try {
      const api = pushAPI(serverId);
      await api.unregisterNativeDevice(saved.token);
      if (saved.voipToken) await api.unregisterNativeDevice(saved.voipToken);
      return true;
    } catch (error) {
      console.error('Failed to remove this device from push notifications:', error);
      return false;
    }
  }
}

/**
 * Opens the conversation of a tapped notification. The server builds URLs for
 * its own bundled client (`/chat/-/<room>[/<thread>]?highlight=<message>`);
 * the app opens the same conversation through its own route helpers.
 */
async function openNotification(data: NativePushData): Promise<void> {
  const origin = typeof data.serverOrigin === 'string' ? data.serverOrigin : null;
  const target = typeof data.url === 'string' ? data.url : null;
  if (!origin || !target) return;

  const server = findServerByUrl(origin);
  if (!server) return;

  let url: URL;
  try {
    url = new URL(target);
  } catch {
    return;
  }
  if (url.origin !== new URL(server.url).origin) return;

  const match = /^\/chat\/-(?:\/([^/]+))?(?:\/([^/]+))?\/?$/.exec(url.pathname);
  if (!match) return;
  const [, roomId, threadId] = match.map((part) => (part ? decodeURIComponent(part) : part));
  await goto(
    roomId
      ? conversationPath(server.id, roomId, {
          threadId,
          messageId: url.searchParams.get('highlight')
        })
      : resolve('/chat/[serverId]', { serverId: serverIdToSegment(server.id) })
  );
}

/** The single owner of native push state in this app. */
export const nativePush = new NativePushState();
