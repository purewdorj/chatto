import { createChattoClient, type ConnectAPIConfig } from '@chatto/client/api/connect';
import { PushNotificationService } from '@chatto/api-types/api/v1/push_notifications_connect';
import type { NativePushPlatform } from '@chatto/api-types/api/v1/push_notifications_pb';
import { PushSubscriptionCleanupService } from '@chatto/api-types/chatto/auth/v1/push_subscription_cleanup_connect';

export type SubscribePushInput = {
  endpoint: string;
  p256dh: string;
  auth: string;
  clientHost: string;
  cleanupToken: string;
  userAgent?: string;
};

/** A native APNs or FCM device registration for the signed-in account. */
export type RegisterNativeDeviceInput = {
  platform: NativePushPlatform;
  token: string;
  /** iOS bundle ID or Android package name; the server accepts only configured apps. */
  appId: string;
  /** True for APNs development builds. Ignored for FCM. */
  apnsSandbox: boolean;
  deviceName?: string;
  cleanupToken: string;
};

export type SubscribePushResult = {
  subscribed: boolean;
};

export type PushRequestOptions = {
  signal?: AbortSignal;
};

export function createPushNotificationAPI(config: ConnectAPIConfig) {
  const client = createChattoClient(PushNotificationService, config);
  // Cleanup proves ownership with the subscription capability, not a session.
  const cleanupClient = createChattoClient(PushSubscriptionCleanupService, {
    baseUrl: config.baseUrl,
    transport: config.transport
  });

  return {
    async subscribe(
      input: SubscribePushInput,
      options: PushRequestOptions = {}
    ): Promise<SubscribePushResult> {
      await client.subscribe(input, { signal: options.signal });
      return {
        subscribed: true
      };
    },

    async unsubscribe(endpoint: string): Promise<boolean> {
      await client.unsubscribe({ endpoint });
      return true;
    },

    async deleteByCapability(
      endpoint: string,
      auth: string,
      cleanupToken: string
    ): Promise<boolean> {
      await cleanupClient.deleteSubscription({ endpoint, auth, cleanupToken });
      return true;
    },

    async registerNativeDevice(
      input: RegisterNativeDeviceInput,
      options: PushRequestOptions = {}
    ): Promise<void> {
      await client.registerNativeDevice(input, { signal: options.signal });
    },

    async unregisterNativeDevice(token: string): Promise<void> {
      await client.unregisterNativeDevice({ token });
    },

    async sendTestNotification(): Promise<boolean> {
      await client.sendTestNotification({});
      return true;
    }
  };
}

export type PushNotificationAPI = ReturnType<typeof createPushNotificationAPI>;
