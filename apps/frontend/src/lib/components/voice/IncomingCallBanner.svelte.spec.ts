import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { page } from 'vitest/browser';
import { flushSync } from 'svelte';
import { IncomingCallState } from '$lib/state/server/incomingCall.svelte';
import IncomingCallBanner from './IncomingCallBanner.svelte';

const mocks = vi.hoisted(() => ({
  stopRingtone: vi.fn(),
  startRingtone: vi.fn(),
  canPlayRingtone: vi.fn(() => true),
  closeNotification: vi.fn(),
  showNotification: vi.fn(),
  goto: vi.fn(async () => {}),
  requestRoomSidebarPanel: vi.fn(),
  join: vi.fn(async () => {}),
  muted: false,
  ui: null as unknown as { incomingCall: IncomingCallState; voiceCall: { join: unknown } }
}));

vi.mock('$app/navigation', async (importOriginal) => ({
  ...(await importOriginal<typeof import('$app/navigation')>()),
  goto: mocks.goto
}));
// The chat layout provides live profiles; the spec shows the fallback values.
vi.mock('$lib/state/userProfiles.svelte', async (importOriginal) => ({
  ...(await importOriginal<typeof import('$lib/state/userProfiles.svelte')>()),
  getLiveDisplayName: (_userId: string, fallback: string) => fallback,
  getLiveAvatarUrl: (_userId: string, fallback: string | null) => fallback,
  getLiveLogin: (_userId: string, fallback: string) => fallback,
  getLiveCustomStatus: () => null,
  getLiveBotOwnerUserId: (_userId: string, fallback: string | null) => fallback
}));
vi.mock('$lib/audio/ringtone', () => ({
  startRingtone: mocks.startRingtone,
  canPlayRingtone: mocks.canPlayRingtone
}));
vi.mock('$lib/notifications/incomingCallNotification', () => ({
  showIncomingCallNotification: mocks.showNotification
}));
vi.mock('$lib/navigation/conversationPath', async (importOriginal) => ({
  ...(await importOriginal<typeof import('$lib/navigation/conversationPath')>()),
  conversationPath: (serverId: string, roomId: string) => `/chat/${serverId}/${roomId}`
}));
vi.mock('$lib/notifications/notificationAlerts', () => ({
  notificationAlertsMuted: () => mocks.muted
}));
vi.mock('$lib/state/appUi.svelte', () => ({
  getAppUiState: () => ({ requestRoomSidebarPanel: mocks.requestRoomSidebarPanel }),
  getRoomSidebarPresentation: () => 'desktop'
}));
vi.mock('$lib/state/server/serverUi', () => ({ serverUi: () => mocks.ui }));

const store = {
  isAuthenticated: true,
  serverInfo: { livekitUrl: 'wss://livekit.test' },
  handleVoiceCallJoinFailed: vi.fn(),
  roomList: {
    rooms: [
      {
        id: 'dm-1',
        members: [{ id: 'alice', login: 'alice', displayName: 'Alice Caller', deleted: false }]
      }
    ]
  }
};

vi.mock('$lib/client', async () => ({
  ...(await import('$lib/test-utils/clientMock')).clientMockDefaults,
  serverRegistry: {
    servers: [{ id: 'server-1' }],
    tryGetStore: () => store
  }
}));

function ring(): void {
  mocks.ui.incomingCall.handleCallStarted({
    roomId: 'dm-1',
    callId: 'call-1',
    callerId: 'alice',
    viewerId: 'bob'
  });
  flushSync();
}

describe('IncomingCallBanner', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.startRingtone.mockReturnValue(mocks.stopRingtone);
    mocks.showNotification.mockReturnValue(mocks.closeNotification);
    mocks.canPlayRingtone.mockReturnValue(true);
    mocks.muted = false;
    mocks.ui = { incomingCall: new IncomingCallState(() => true), voiceCall: { join: mocks.join } };
  });
  afterEach(() => mocks.ui.incomingCall.clear());

  it('shows nothing and stays silent without an incoming call', async () => {
    render(IncomingCallBanner);
    await expect.element(page.getByTestId('incoming-call-banner')).not.toBeInTheDocument();
    expect(mocks.startRingtone).not.toHaveBeenCalled();
  });

  it('rings and names the caller, then stops when the call ends', async () => {
    render(IncomingCallBanner);
    ring();
    await expect.element(page.getByText('Alice Caller')).toBeVisible();
    expect(mocks.startRingtone).toHaveBeenCalledOnce();

    mocks.ui.incomingCall.handleCallEnded('dm-1', 'call-1');
    flushSync();
    await expect.element(page.getByTestId('incoming-call-banner')).not.toBeInTheDocument();
    expect(mocks.stopRingtone).toHaveBeenCalledOnce();
  });

  it('does not show a system notification when the ringtone can play in a visible tab', async () => {
    render(IncomingCallBanner);
    ring();
    await expect.element(page.getByText('Alice Caller')).toBeVisible();
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('shows a system notification when the browser blocks the ringtone', async () => {
    mocks.canPlayRingtone.mockReturnValue(false);
    render(IncomingCallBanner);
    ring();
    await expect.element(page.getByText('Alice Caller')).toBeVisible();
    expect(mocks.showNotification).toHaveBeenCalledWith({
      title: 'Incoming call',
      body: 'Alice Caller'
    });

    mocks.ui.incomingCall.handleCallEnded('dm-1', 'call-1');
    flushSync();
    expect(mocks.closeNotification).toHaveBeenCalledOnce();
  });

  it('shows the banner without sound in Do Not Disturb', async () => {
    mocks.muted = true;
    render(IncomingCallBanner);
    ring();
    await expect.element(page.getByText('Alice Caller')).toBeVisible();
    expect(mocks.startRingtone).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('stops ringing on decline without joining', async () => {
    render(IncomingCallBanner);
    ring();
    await page.getByRole('button', { name: 'Decline' }).click();
    await expect.element(page.getByTestId('incoming-call-banner')).not.toBeInTheDocument();
    expect(mocks.stopRingtone).toHaveBeenCalledOnce();
    expect(mocks.join).not.toHaveBeenCalled();
  });

  it('opens the conversation call panel and joins the call', async () => {
    render(IncomingCallBanner);
    ring();
    await page.getByRole('button', { name: 'Join call' }).click();
    await vi.waitFor(() => expect(mocks.join).toHaveBeenCalledWith('wss://livekit.test', 'dm-1'));
    expect(mocks.requestRoomSidebarPanel).toHaveBeenCalledWith(
      'server-1',
      'dm-1',
      'call',
      'desktop'
    );
    expect(mocks.goto).toHaveBeenCalledWith('/chat/server-1/dm-1');
    expect(mocks.stopRingtone).toHaveBeenCalledOnce();
  });
});
