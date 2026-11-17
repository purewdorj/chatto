import { beforeEach, describe, expect, it, vi } from 'vitest';
const mocks = vi.hoisted(() => ({
  origin: undefined as { id: string } | undefined,
  ensureSetunariServer: vi.fn<() => Promise<string | undefined>>()
}));
vi.mock('$lib/serverCatalogue', () => ({ ensureSetunariServer: mocks.ensureSetunariServer }));
vi.mock('$lib/navigation', () => ({ serverIdToSegment: (serverId: string) => serverId }));
vi.mock('$lib/client', () => ({
  serverRegistry: {
    get originServer() {
      return mocks.origin;
    }
  }
}));
import { load } from './+page';
import { load as loginLoad } from './login/+page';

async function expectRedirect(
  url: string,
  user: { id: string } | null,
  location: string
): Promise<void> {
  await expect(
    load({
      parent: async () => ({ user }),
      url: new URL(url)
    } as never)
  ).rejects.toMatchObject({ status: 302, location });
}

describe('root landing load', () => {
  beforeEach(() => {
    mocks.origin = { id: 'origin' };
    mocks.ensureSetunariServer.mockReset();
    mocks.ensureSetunariServer.mockResolvedValue(undefined);
  });
  it('redirects unauthenticated visitors to login before rendering', async () => {
    await expectRedirect('https://chat.example.test/', null, '/login');
  });

  it('preserves query parameters when redirecting authenticated visitors to chat', async () => {
    await expectRedirect(
      'https://chat.example.test/?welcome=true',
      { id: 'user-1' },
      '/chat?welcome=true'
    );
  });

  it.each(['https://static.example/', 'chatto://desktop/'])(
    'opens the directory on a host without an origin server: %s',
    async (url) => {
      mocks.origin = undefined;
      await expectRedirect(url, null, '/chat/servers');
    }
  );

  // Setunari: the desktop app starts with the company server.
  it('opens the company server on a host without an origin server', async () => {
    mocks.origin = undefined;
    mocks.ensureSetunariServer.mockResolvedValue('setunari-server');
    await expectRedirect('chatto://desktop/', null, '/chat/setunari-server');
  });

  it('does not add the company server when the origin is a Chatto server', async () => {
    await expectRedirect('https://chat.example.test/', null, '/login');
    expect(mocks.ensureSetunariServer).not.toHaveBeenCalled();
  });

  it('keeps an existing origin server on login when discovery is unavailable', async () => {
    await expectRedirect('https://chat.example.test/', null, '/login');
  });

  it('redirects the old standalone login entry to the directory', async () => {
    mocks.origin = undefined;
    await expect(
      loginLoad({
        parent: async () => ({ user: null }),
        url: new URL('https://static.example/login')
      } as never)
    ).rejects.toMatchObject({ location: '/chat/servers' });
  });

  it('preserves an explicit authorization continuation when discovery is unavailable', async () => {
    mocks.origin = undefined;
    await expect(
      loginLoad({
        parent: async () => ({ user: null }),
        url: new URL('https://server.example/login?redirect=%2Foauth%2Fauthorize%3Fstate%3Dexample')
      } as never)
    ).resolves.toMatchObject({ redirectUrl: '/oauth/authorize?state=example' });
  });
});
