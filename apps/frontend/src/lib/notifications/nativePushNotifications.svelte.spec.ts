import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Code, ConnectError } from '@connectrpc/connect';
import { PushNotificationService } from '@chatto/api-types/api/v1/push_notifications_connect';
import { NativePushPlatform } from '@chatto/api-types/api/v1/push_notifications_pb';
import { fakeServer, mockService, receivedRequest } from '@chatto/client/testing/fakeServer';

const mocks = vi.hoisted(() => ({
  service: null as unknown as ReturnType<typeof mockService<typeof PushNotificationService>>,
  permission: 'granted' as string,
  token: 'device-token' as string | Error,
  voipToken: null as string | null,
  opened: null as ((data: Record<string, unknown>) => void) | null,
  goto: vi.fn(),
  stores: {
    origin: {
      isAuthenticated: true,
      accountId: 'origin-user' as string | null,
      serverInfo: { nativePushPlatforms: [] as number[] }
    },
    remote: {
      isAuthenticated: true,
      accountId: 'remote-user' as string | null,
      projection: { rooms: new Map() },
      serverInfo: { nativePushPlatforms: [] as number[] }
    }
  }
}));

vi.mock('$app/navigation', async (importOriginal) => ({
  ...(await importOriginal<typeof import('$app/navigation')>()),
  goto: mocks.goto
}));

vi.mock('$lib/desktop/nativePush', async () => {
  const { NativePushPlatform } = await import('@chatto/api-types/api/v1/push_notifications_pb');
  return {
    nativePushPlatform: () => NativePushPlatform.APNS,
    nativePlatformName: () => 'iOS',
    checkNativePushPermission: async () => mocks.permission,
    requestNativePushPermission: async () => mocks.permission,
    nativePushToken: async () => {
      if (mocks.token instanceof Error) throw mocks.token;
      return mocks.token;
    },
    onNativePushOpened: async (listener: (data: Record<string, unknown>) => void) => {
      mocks.opened = listener;
      return () => {};
    }
  };
});

vi.mock('$lib/desktop/nativeCalls', () => ({
  nativeVoipToken: async () => mocks.voipToken,
  onNativeVoipToken: async () => () => {}
}));

vi.mock('$lib/client', async () => ({
  ...(await import('$lib/test-utils/clientMock')).clientMockDefaults,
  serverRegistry: {
    servers: [
      { id: 'origin', url: 'https://app.test' },
      { id: 'remote', url: 'https://remote.example.com' }
    ],
    isOriginServer: (serverId: string) => serverId === 'origin',
    tryGetStore: (serverId: 'origin' | 'remote') => mocks.stores[serverId],
    getServer: (serverId: string) => ({
      id: serverId,
      url: serverId === 'origin' ? 'https://app.test' : 'https://remote.example.com'
    })
  },
  serverConnectionManager: {
    getClient: () => ({
      getAPI: (factory: (config: never) => unknown) =>
        factory(
          fakeServer((router) => router.service(PushNotificationService, mocks.service)) as never
        )
    })
  }
}));

vi.mock('$lib/serverCatalogue', async (importOriginal) => ({
  ...(await importOriginal<typeof import('$lib/serverCatalogue')>()),
  findServerByUrl: (url: string) =>
    new URL(url).origin === 'https://remote.example.com'
      ? { id: 'remote', url: 'https://remote.example.com' }
      : undefined
}));

const { nativePush } = await import('./nativePushNotifications.svelte');

describe('native push notifications', () => {
  beforeEach(() => {
    mocks.service = mockService(PushNotificationService);
    mocks.service.registerNativeDevice.mockReturnValue({});
    mocks.service.unregisterNativeDevice.mockReturnValue({});
    mocks.permission = 'granted';
    mocks.token = 'device-token';
    mocks.voipToken = null;
    mocks.stores.origin.serverInfo.nativePushPlatforms = [];
    mocks.stores.remote.serverInfo.nativePushPlatforms = [NativePushPlatform.APNS];
    localStorage.clear();
    vi.stubGlobal('chattoNativeApp', { appId: 'mn.setunari.chatto.mobile', apnsSandbox: true });
  });

  afterEach(async () => {
    await nativePush.disable().catch(() => {});
    vi.unstubAllGlobals();
    mocks.goto.mockReset();
  });

  it('registers only with servers that deliver to this platform', async () => {
    await nativePush.enable();

    expect(mocks.service.registerNativeDevice).toHaveBeenCalledTimes(1);
    expect(receivedRequest(mocks.service.registerNativeDevice)).toMatchObject({
      platform: NativePushPlatform.APNS,
      token: 'device-token',
      appId: 'mn.setunari.chatto.mobile',
      apnsSandbox: true,
      deviceName: 'iOS'
    });
    expect(receivedRequest(mocks.service.registerNativeDevice)?.cleanupToken).toHaveLength(32);
    expect(nativePush.isRegistered('remote', 'remote-user')).toBe(true);
    expect(nativePush.isRegistered('origin', 'origin-user')).toBe(false);
  });

  it('also registers the VoIP token with a server that rings through VoIP', async () => {
    mocks.voipToken = 'voip-token';
    mocks.stores.remote.serverInfo.nativePushPlatforms = [
      NativePushPlatform.APNS,
      NativePushPlatform.APNS_VOIP
    ];
    await nativePush.enable();

    expect(receivedRequest(mocks.service.registerNativeDevice, 1)).toMatchObject({
      platform: NativePushPlatform.APNS_VOIP,
      token: 'voip-token',
      appId: 'mn.setunari.chatto.mobile',
      apnsSandbox: true
    });
    expect(nativePush.isVoipRegistered('remote')).toBe(true);

    await nativePush.disable();
    expect(
      mocks.service.unregisterNativeDevice.mock.calls.map(([request]) => request.token)
    ).toEqual(['device-token', 'voip-token']);
  });

  it('does not register the VoIP token with a server without VoIP calls', async () => {
    mocks.voipToken = 'voip-token';
    await nativePush.enable();

    expect(mocks.service.registerNativeDevice).toHaveBeenCalledTimes(1);
    expect(nativePush.isVoipRegistered('remote')).toBe(false);
  });

  it('does not save again while the registration is current', async () => {
    await nativePush.enable();
    await nativePush.refresh();

    expect(mocks.service.registerNativeDevice).toHaveBeenCalledTimes(1);
  });

  it('does nothing without notification permission', async () => {
    mocks.permission = 'denied';
    await nativePush.enable();

    expect(mocks.service.registerNativeDevice).not.toHaveBeenCalled();
    expect(nativePush.on).toBe(false);
  });

  it('records a failed device registration for each server', async () => {
    mocks.token = new Error('Firebase is not configured');
    await nativePush.enable();

    expect(mocks.service.registerNativeDevice).not.toHaveBeenCalled();
    expect(nativePush.failure('remote')).toBe('Firebase is not configured');
  });

  it('records a server rejection', async () => {
    mocks.service.registerNativeDevice.mockImplementation(() => {
      throw new ConnectError('app is not configured', Code.InvalidArgument);
    });
    await nativePush.enable();

    expect(nativePush.isRegistered('remote', 'remote-user')).toBe(false);
    expect(nativePush.failure('remote')).not.toBeNull();
  });

  it('removes the device from every server when turned off', async () => {
    await nativePush.enable();
    await nativePush.disable();

    expect(receivedRequest(mocks.service.unregisterNativeDevice)).toMatchObject({
      token: 'device-token'
    });
    expect(nativePush.on).toBe(false);
    expect(localStorage.getItem('chatto:native-push-disabled')).toBe('true');
  });

  it('removes the device from a server before leaving it', async () => {
    await nativePush.enable();
    await nativePush.unsubscribeBeforeLeaving('remote');

    expect(mocks.service.unregisterNativeDevice).toHaveBeenCalledTimes(1);
    expect(nativePush.isRegistered('remote', 'remote-user')).toBe(false);
  });

  describe('opening a notification', () => {
    beforeEach(() => nativePush.listenForOpenedNotifications());

    it('opens the thread and message on the matching server', async () => {
      mocks.opened?.({
        serverOrigin: 'https://remote.example.com',
        url: 'https://remote.example.com/chat/-/room-1/thread-1?highlight=message-1'
      });

      await vi.waitFor(() =>
        expect(mocks.goto).toHaveBeenCalledWith(
          '/chat/remote.example.com/room-1/thread-1/m/message-1'
        )
      );
    });

    it('opens the server for a notification without a room', async () => {
      mocks.opened?.({
        serverOrigin: 'https://remote.example.com',
        url: 'https://remote.example.com/chat/-'
      });

      await vi.waitFor(() => expect(mocks.goto).toHaveBeenCalledWith('/chat/remote.example.com'));
    });

    it.each([
      ['an unknown server', 'https://unknown.example.com', 'https://unknown.example.com/chat/-/r'],
      [
        'a URL on another origin',
        'https://remote.example.com',
        'https://evil.example.com/chat/-/r'
      ],
      ['a path outside the chat', 'https://remote.example.com', 'https://remote.example.com/admin']
    ])('ignores %s', async (_, serverOrigin, url) => {
      mocks.opened?.({ serverOrigin, url });
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(mocks.goto).not.toHaveBeenCalled();
    });
  });
});
