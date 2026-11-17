import { toastError } from '$lib/utils/errorMessage';
import { serverUi } from '$lib/state/server/serverUi';
import { RoomKind } from '@chatto/api-types/api/v1/rooms_pb';
import { accountNameToken } from '@chatto/client/timeline/accountName';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';
import { onDestroy, tick, untrack } from 'svelte';
import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { createMemberDirectoryAPI } from '@chatto/client/api/memberDirectory';
import {
  createMessageSearchAPI,
  MessageSearchOrder,
  type MessageSearchResult
} from '@chatto/client/api/messageSearch';
import { useDebounce } from '$lib/hooks/useDebounce.svelte';
import { startDMWith } from '$lib/dm/startDM';
import { conversationPath } from '$lib/navigation/conversationPath';
import { m } from '$lib/i18n/messages';
import { buildMessageLinkPath } from '$lib/chatLinks';
import { serverIdToSegment } from '$lib/navigation';
import {
  buildDirectMessagePresentation,
  type UserAvatarUserView
} from '@chatto/client/timeline/users';
import { directMessageLabels } from '$lib/render/directMessageLabels';
import { quickSwitcher } from '$lib/state/globals.svelte';
import { recentQuickSwitcher } from '$lib/state/recentQuickSwitcher.svelte';
import { serverRegistry, serverConnectionManager } from '$lib/client';
import { isNavigationVisibleRoom } from '$lib/state/server/navigation';
import { scoreItem } from './quickSwitcherSearch';

export type QuickSwitcherAvatarUser = Pick<
  UserAvatarUserView,
  'id' | 'login' | 'displayName' | 'deleted' | 'isBot' | 'presenceStatus' | 'avatarUrl'
>;

type ServerLogo = { name: string; logoUrl?: string | null };

export type QuickSwitcherItem = {
  kind: 'room' | 'dm' | 'destination' | 'server' | 'user' | 'message';
  id: string;
  label: string;
  detail: string;
  serverId: string;
  serverName: string;
  serverLogo?: ServerLogo;
  participants?: QuickSwitcherAvatarUser[];
  currentUserId?: string;
  targetUserId?: string;
  /** Participant names and logins used for matching without changing the row label. */
  searchTerms?: string[];
  href?: string;
  icon?: string;
  message?: MessageSearchResult;
  score: number;
};

const SEARCH_DEBOUNCE_MS = 200;
const MESSAGE_SEARCH_SERVER_TIMEOUT_MS = 3_000;
/** Server user-directory rows requested per server for an ordinary query (Setunari). */
const DIRECTORY_USER_LIMIT = 8;
/** Message results shown after destinations and people for an ordinary query (Setunari). */
const MIXED_MESSAGE_LIMIT = 5;
/** Ordinary queries shorter than this do not start a message search (Setunari). */
const MIXED_MESSAGE_MIN_LENGTH = 2;

class SearchChannel<T> {
  items = $state.raw<T[]>([]);
  loading = $state(false);

  #requestId = 0;
  #debounce = useDebounce();

  schedule(query: string | null, load: (query: string, requestId: number) => void): void {
    this.#debounce.cancel();
    const requestId = ++this.#requestId;
    if (!query) {
      this.items = [];
      this.loading = false;
      return;
    }

    this.loading = true;
    this.#debounce.run(() => load(query, requestId), SEARCH_DEBOUNCE_MS);
  }

  replace(requestId: number, items: T[]): boolean {
    if (!this.isCurrent(requestId)) return false;
    this.items = items;
    return true;
  }

  finish(requestId: number): void {
    if (this.isCurrent(requestId)) this.loading = false;
  }

  isCurrent(requestId: number): boolean {
    return requestId === this.#requestId;
  }

  fence(): void {
    this.#debounce.cancel();
    this.#requestId++;
    this.items = [];
    this.loading = false;
  }
}

/**
 * Owns the Quick Switcher's per-mount catalog, search, privacy, selection, and
 * navigation lifecycle. DOM focus and dialog behavior remain in the component.
 */
export class QuickSwitcherModel {
  query = $state('');
  selectedIndex = $state(0);

  #allItems = $state.raw<QuickSwitcherItem[]>([]);
  #catalogLoading = $state(false);
  #messageSearch = new SearchChannel<QuickSwitcherItem>();
  #messageSearchServerKey = '';
  /**
   * Setunari: people from each server's user directory (`UserService.ListUsers`), so an
   * ordinary query finds every member, not only users already known to the live client.
   */
  #directoryUsers = new SearchChannel<QuickSwitcherItem>();
  /** Per server: users who already have a visible one-to-one or self DM row. */
  #dmUserIds = new SvelteMap<string, Set<string>>();

  constructor(private readonly onNextUnread?: () => Promise<void> | undefined) {
    onDestroy(() => this.deactivate());
  }

  kindLabels = $derived<Record<QuickSwitcherItem['kind'], string>>({
    destination: m('quick_switcher.kind.destination'),
    server: m('quick_switcher.kind.server'),
    room: m('quick_switcher.kind.room'),
    dm: m('quick_switcher.kind.dm'),
    user: m('quick_switcher.kind.user'),
    message: m('quick_switcher.kind.message')
  });

  filtered = $derived.by(() => {
    const raw = this.query.trim();
    const recentUrls = recentQuickSwitcher.urls;
    const recentSet = new SvelteSet(recentUrls);

    if (raw.startsWith('?')) return this.#messageSearch.items;

    if (!raw) {
      const recent: QuickSwitcherItem[] = [];
      const rest: QuickSwitcherItem[] = [];
      for (const item of this.#allItems) {
        if (item.kind === 'user') continue;
        const url = this.#itemUrl(item);
        (url && recentSet.has(url) ? recent : rest).push(item);
      }
      recent.sort(
        (a, b) => recentUrls.indexOf(this.#itemUrl(a)!) - recentUrls.indexOf(this.#itemUrl(b)!)
      );
      const kindOrder: Record<QuickSwitcherItem['kind'], number> = {
        destination: 0,
        server: 1,
        room: 2,
        dm: 3,
        user: 4,
        message: 5
      };
      rest.sort((a, b) => kindOrder[a.kind] - kindOrder[b.kind] || a.label.localeCompare(b.label));
      return [...recent, ...rest];
    }

    const isChannelFilter = raw.startsWith('#');
    const query = isChannelFilter ? raw.slice(1) : raw;
    const pool = isChannelFilter
      ? this.#allItems.filter((item) => item.kind === 'room')
      : this.#allItems;
    if (isChannelFilter && !query) {
      return [...pool].sort((a, b) => a.label.localeCompare(b.label));
    }

    const scored: QuickSwitcherItem[] = [];
    for (const item of pool) {
      const matchScore = scoreItem(query, item);
      if (matchScore === null) continue;
      const recentIndex = recentUrls.indexOf(this.#itemUrl(item) ?? '');
      scored.push({
        ...item,
        score: matchScore + (recentIndex === -1 ? 0 : 300 - recentIndex * 20)
      });
    }
    // Known users supplement navigation; even an exact user match follows conversations.
    scored.sort(
      (a, b) => Number(a.kind === 'user') - Number(b.kind === 'user') || b.score - a.score
    );
    if (isChannelFilter) return scored;

    // Setunari: add directory people the client does not know yet, then message matches,
    // so one query searches channels, DMs, all people, and messages together.
    const seenUsers = new SvelteSet(
      scored.filter((item) => item.kind === 'user').map((item) => `${item.serverId}\0${item.id}`)
    );
    const directoryUsers = this.#directoryUsers.items.filter((item) => {
      const key = `${item.serverId}\0${item.id}`;
      if (seenUsers.has(key) || this.#dmUserIds.get(item.serverId)?.has(item.id)) return false;
      seenUsers.add(key);
      return true;
    });
    return [
      ...scored,
      ...directoryUsers,
      ...this.#messageSearch.items.slice(0, MIXED_MESSAGE_LIMIT)
    ];
  });

  get loading(): boolean {
    const raw = this.query.trim();
    if (raw.startsWith('?')) return this.#messageSearch.loading;
    if (raw.startsWith('#') || !raw) return this.#catalogLoading;
    return this.#catalogLoading || this.#directoryUsers.loading || this.#messageSearch.loading;
  }

  activate(): void {
    this.query = '';
    this.selectedIndex = 0;
    this.#allItems = [];
    this.#catalogLoading = false;
    this.#messageSearch.fence();
    this.#directoryUsers.fence();
  }

  deactivate(): void {
    this.#allItems = [];
    this.#catalogLoading = false;
    this.#messageSearch.fence();
    this.#directoryUsers.fence();
  }

  setQuery(raw: string): void {
    this.query = raw;
    this.selectedIndex = 0;

    this.#scheduleMessageSearch(raw);
    this.#scheduleDirectorySearch(raw);
  }

  moveSelection(delta: number): void {
    this.selectedIndex = Math.max(
      0,
      Math.min(this.selectedIndex + delta, this.filtered.length - 1)
    );
  }

  selectIndex(index: number): void {
    this.selectedIndex = index;
  }

  selectCurrent(): void {
    const item = this.filtered[this.selectedIndex];
    if (item) void this.select(item);
  }

  async select(item: QuickSwitcherItem): Promise<void> {
    if (item.kind === 'destination' && item.id === 'next-unread') {
      quickSwitcher.close();
      await tick();
      await this.onNextUnread?.();
      return;
    }
    if (item.kind === 'user') {
      const store = serverRegistry.tryGetStore(item.serverId);
      // Recheck the live scope before an action from a row that may have become stale.
      if (
        !store?.isAuthenticated ||
        !store.realtimeSync.hasUsableProjection ||
        !store.permissions.canStartDMs
      )
        return;
      const known = item.targetUserId
        ? store.projection.users.get(item.targetUserId)?.user
        : undefined;
      if (known && (known.deleted || known.deactivated)) return;
      // Setunari: directory rows can name users the live client has not loaded yet.
      const userId = known?.id ?? item.participants?.[0]?.id;
      if (!userId || item.participants?.[0]?.deleted) return;
      quickSwitcher.close();
      try {
        await startDMWith(item.serverId, userId);
      } catch (error) {
        toastError(error, m('quick_switcher.start_dm_failed'));
      }
      return;
    }
    quickSwitcher.close();

    const url = this.#itemUrl(item);
    if (!url) return;
    if (item.kind !== 'message') recentQuickSwitcher.record(url);
    await goto(resolve(url as '/'));
  }

  groupHeader(index: number): string | null {
    const item = this.filtered[index];
    if (!item) return null;
    const previous = index > 0 ? this.filtered[index - 1] : null;
    if (this.query.trim()) {
      // Setunari: label the people and message sections of a mixed result list.
      if (this.query.trim().startsWith('?')) return null;
      const grouped = item.kind === 'user' || item.kind === 'message';
      return grouped && previous?.kind !== item.kind ? this.kindLabels[item.kind] : null;
    }
    const recent = this.#isRecent(item);
    const previousRecent = previous ? this.#isRecent(previous) : false;

    if (!recent && (index === 0 || previousRecent)) return this.kindLabels[item.kind];
    if (recent && (index === 0 || !previousRecent)) return m('quick_switcher.recent');
    if (!recent && previous && previous.kind !== item.kind) return this.kindLabels[item.kind];
    return null;
  }

  /** Track the canonical cross-server navigation inputs and rebuild the local catalog. */
  syncCatalog(): void {
    if (!quickSwitcher.visible) return;
    void serverRegistry.servers;
    for (const instance of serverRegistry.servers) {
      const store = serverRegistry.tryGetStore(instance.id);
      void store?.isAuthenticated;
      void store?.realtimeSync.hasUsableProjection;
      void store?.permissions.canStartDMs;
      if (store) for (const member of store.projection.users.values()) void member;
      void (store && serverUi(store).navigation.rooms);
      void (store && serverUi(store).navigation.isInitialLoading);
    }
    untrack(() => this.#loadCatalog());
  }

  /** Subscribe to each server's message-search privacy boundary for this open palette. */
  syncPrivacy(): (() => void) | undefined {
    if (!quickSwitcher.visible) return;
    const instances = serverRegistry.servers;
    const serverKey = instances.map((instance) => instance.id).join('\0');
    const stores = instances.flatMap((instance) => {
      const store = serverRegistry.tryGetStore(instance.id);
      return store ? [{ serverId: instance.id, store }] : [];
    });

    return untrack(() => {
      if (this.#messageSearchServerKey && this.#messageSearchServerKey !== serverKey) {
        this.#restartMessageSearch(true);
      }
      this.#messageSearchServerKey = serverKey;
      const unsubscribes = stores.map(({ serverId, store }) =>
        serverUi(store).messageSearch.subscribePrivacyInvalidation((matches, force) => {
          if (!quickSwitcher.visible) return;
          const affected = this.#messageSearch.items.some(
            (item) => item.serverId === serverId && item.message && matches(item.message)
          );
          this.#restartMessageSearch(force || affected);
        })
      );
      return () => unsubscribes.forEach((unsubscribe) => unsubscribe());
    });
  }

  #restartMessageSearch(clearResults: boolean): void {
    if (clearResults) this.#messageSearch.fence();
    this.#scheduleMessageSearch(this.query);
  }

  #scheduleMessageSearch(raw: string): void {
    const trimmed = raw.trim();
    // Setunari: ordinary queries also search messages; `?` still means messages only.
    const query = trimmed.startsWith('?')
      ? trimmed.slice(1).trim()
      : trimmed.startsWith('#') || trimmed.length < MIXED_MESSAGE_MIN_LENGTH
        ? ''
        : trimmed;
    this.#messageSearch.schedule(
      quickSwitcher.visible && query ? query : null,
      (search, requestId) => void this.#loadMessageResults(search, requestId)
    );
  }

  #scheduleDirectorySearch(raw: string): void {
    const trimmed = raw.trim();
    const query = trimmed.startsWith('?') || trimmed.startsWith('#') ? '' : trimmed;
    this.#directoryUsers.schedule(
      quickSwitcher.visible && query ? query : null,
      (search, requestId) => void this.#loadDirectoryUsers(search, requestId)
    );
  }

  /** Setunari: ask every authenticated server's user directory for matching people. */
  async #loadDirectoryUsers(search: string, requestId: number): Promise<void> {
    const instances = [...serverRegistry.servers];
    const multiInstance = instances.length > 1;
    const searches = instances.map(async (instance): Promise<QuickSwitcherItem[]> => {
      const store = serverRegistry.tryGetStore(instance.id);
      if (!store?.isAuthenticated || !store.permissions.canStartDMs) return [];
      const serverName = store.serverInfo.name || instance.name || getHostname(instance.url);
      const serverLabel = multiInstance ? serverName : '';
      const page = await serverConnectionManager
        .getClient(instance.id)
        .getAPI(createMemberDirectoryAPI)
        .listUsers(search, DIRECTORY_USER_LIMIT, 0);
      return page.members
        .filter((user) => user.id && !user.deleted)
        .map((user) => ({
          kind: 'user' as const,
          id: user.id,
          targetUserId: user.id,
          label: user.displayName || user.login,
          detail: [user.login ? `@${user.login}` : '', serverLabel].filter(Boolean).join(' · '),
          serverId: instance.id,
          serverName,
          participants: [user],
          searchTerms: [user.displayName, user.login],
          score: 0
        }));
    });
    const results = await Promise.all(
      searches.map((promise) => resolveWithin(promise, MESSAGE_SEARCH_SERVER_TIMEOUT_MS, []))
    );
    const items = results.flat().sort((a, b) => a.label.localeCompare(b.label));
    this.#directoryUsers.replace(requestId, items);
    this.#directoryUsers.finish(requestId);
  }

  #loadCatalog(): void {
    const instances = serverRegistry.servers;
    const multiInstance = instances.length > 1;
    const items: QuickSwitcherItem[] = [];
    this.#catalogLoading = instances.some((instance) => {
      const store = serverRegistry.tryGetStore(instance.id);
      return store?.isAuthenticated && serverUi(store).navigation.isInitialLoading;
    });

    for (const instance of instances) {
      const store = serverRegistry.tryGetStore(instance.id);
      const serverName = store?.serverInfo.name || instance.name || getHostname(instance.url);
      const serverLabel = multiInstance ? serverName : '';
      const currentUserId = store?.viewerId ?? undefined;
      const logo: ServerLogo = { name: serverName, logoUrl: store?.serverInfo.iconUrl ?? null };
      const directMessageUserIds = new SvelteSet<string>();
      this.#dmUserIds.set(instance.id, directMessageUserIds);

      items.push({
        kind: 'server',
        id: `server-${instance.id}`,
        label: logo.name,
        detail: '',
        serverId: instance.id,
        serverName: logo.name,
        serverLogo: logo,
        href: resolve('/chat/[serverId]/overview', { serverId: serverIdToSegment(instance.id) }),
        score: 0
      });

      for (const room of store ? serverUi(store).navigation.rooms : []) {
        if (room.type === RoomKind.DM) {
          if (!isNavigationVisibleRoom(room)) continue;
          const participants = room.members;
          const presentation = buildDirectMessagePresentation(
            participants,
            currentUserId,
            directMessageLabels()
          );
          // Count full membership, not visible avatars: a group can lose profile data.
          const memberIds =
            store?.projection.rooms.get(room.id)?.memberUserIds ??
            room.members.map((user) => user.id);
          if (currentUserId && memberIds.includes(currentUserId) && memberIds.length <= 2) {
            for (const userId of memberIds) {
              if (userId !== currentUserId || memberIds.length === 1)
                directMessageUserIds.add(userId);
            }
          }
          items.push({
            kind: 'dm',
            id: room.id,
            label: presentation.label,
            detail: serverLabel,
            serverId: instance.id,
            serverName,
            participants: presentation.visibleParticipants,
            searchTerms: presentation.visibleParticipants.flatMap((user) => [
              user.displayName,
              user.login
            ]),
            currentUserId,
            score: 0
          });
          continue;
        }

        if (!room.viewerIsMember) continue;
        items.push({
          kind: 'room',
          id: room.id,
          label: room.name,
          detail: serverLabel || logo.name,
          serverId: instance.id,
          serverName,
          serverLogo: logo,
          score: 0
        });
      }

      if (
        store?.isAuthenticated &&
        store.realtimeSync.hasUsableProjection &&
        store.permissions.canStartDMs
      ) {
        for (const id of store.projection.users.keys()) {
          const user = store.projection.users.view(id);
          if (!user?.id || user.deleted || user.deactivated || directMessageUserIds.has(user.id))
            continue;
          items.push({
            kind: 'user',
            id: user.id,
            targetUserId: user.id,
            label: user.displayName || user.login,
            detail: [user.login ? `@${user.login}` : '', serverLabel].filter(Boolean).join(' · '),
            serverId: instance.id,
            serverName,
            participants: [user],
            searchTerms: [user.displayName, user.login],
            score: 0
          });
        }
      }
    }

    if (this.onNextUnread)
      items.push({
        kind: 'destination',
        id: 'next-unread',
        label: m('chat.notifications.next_unread_source'),
        detail: '',
        serverId: '',
        serverName: '',
        icon: 'icon-[uil--bell]',
        score: 0
      });
    items.push({
      kind: 'destination',
      id: 'notifications',
      label: m('ui.notifications'),
      detail: '',
      serverId: '',
      serverName: '',
      href: resolve('/chat/notifications'),
      icon: 'icon-[uil--bell]',
      score: 0
    });
    // Catalogue updates must not move keyboard selection to a different destination.
    const selected = this.filtered[this.selectedIndex];
    this.#allItems = items;
    const selectedIndex = selected
      ? this.filtered.findIndex(
          (item) =>
            item.serverId === selected.serverId &&
            item.kind === selected.kind &&
            item.id === selected.id
        )
      : -1;
    this.selectedIndex = selectedIndex >= 0 ? selectedIndex : 0;
  }

  async #loadMessageResults(search: string, requestId: number): Promise<void> {
    const instances = [...serverRegistry.servers];
    const resultsByServer: Record<string, QuickSwitcherItem[]> = {};
    const publish = (serverId: string, items: QuickSwitcherItem[]) => {
      if (!this.#messageSearch.isCurrent(requestId)) return;
      if (!serverRegistry.servers.some((instance) => instance.id === serverId)) return;
      const selected = this.#messageSearch.items[this.selectedIndex];
      resultsByServer[serverId] = items;
      const accumulated = Object.values(resultsByServer)
        .flat()
        .sort(
          (a, b) =>
            b.score - a.score ||
            (b.message?.createdAt ?? '').localeCompare(a.message?.createdAt ?? '') ||
            a.id.localeCompare(b.id)
        );
      if (!this.#messageSearch.replace(requestId, accumulated)) return;
      // Setunari: in a mixed result list, messages come last and must not move the selection.
      if (!this.query.trim().startsWith('?')) return;
      const preservedIndex = selected
        ? accumulated.findIndex(
            (item) => item.serverId === selected.serverId && item.id === selected.id
          )
        : -1;
      this.selectedIndex = preservedIndex >= 0 ? preservedIndex : 0;
    };

    const searches = instances.map(async (instance): Promise<QuickSwitcherItem[]> => {
      const store = serverRegistry.tryGetStore(instance.id);
      if (!store?.serverInfo.canConnect) return [];
      await serverUi(store).messageSearch.availability.ensureStatus();
      if (!serverUi(store).messageSearch.availability.available) return [];

      const serverName = store.serverInfo.name || instance.name || getHostname(instance.url);
      try {
        const page = await serverConnectionManager
          .getClient(instance.id)
          .getAPI(createMessageSearchAPI)
          .searchMessages({
            query: search,
            order: MessageSearchOrder.RELEVANCE,
            pageSize: 10
          });
        return page.results.map((message) => ({
          kind: 'message',
          id: message.id,
          label: message.body,
          detail: [
            message.actor ? accountNameToken(0) : undefined,
            message.roomName ? `#${message.roomName}` : null,
            serverName
          ]
            .filter(Boolean)
            .join(' · '),
          serverId: instance.id,
          serverName,
          message,
          score: message.relevanceScore
        }));
      } catch {
        return [];
      }
    });
    const boundedSearches = searches.map((promise) =>
      resolveWithin(promise, MESSAGE_SEARCH_SERVER_TIMEOUT_MS, [])
    );
    boundedSearches.forEach((promise, index) => {
      const serverId = instances[index]!.id;
      void promise.then(
        (items) => publish(serverId, items),
        () => publish(serverId, [])
      );
    });
    await Promise.all(boundedSearches);
    this.#messageSearch.finish(requestId);
  }

  #itemUrl(item: QuickSwitcherItem): string | undefined {
    if ((item.kind === 'destination' || item.kind === 'server') && item.href) return item.href;
    if (item.kind === 'dm' || item.kind === 'room') {
      return conversationPath(item.serverId, item.id);
    }
    if (item.kind === 'message' && item.message) {
      return buildMessageLinkPath(
        item.serverId,
        item.message.roomId,
        item.message.id,
        item.message.threadRootEventId
      );
    }
  }

  #isRecent(item: QuickSwitcherItem): boolean {
    const url = this.#itemUrl(item);
    return url !== undefined && recentQuickSwitcher.urls.includes(url);
  }
}

function getHostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function resolveWithin<T>(promise: Promise<T>, timeoutMs: number, fallback: T): Promise<T> {
  return new Promise((resolve) => {
    let settled = false;
    const timeout = setTimeout(() => {
      settled = true;
      resolve(fallback);
    }, timeoutMs);
    void promise.then(
      (value) => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        resolve(value);
      },
      () => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        resolve(fallback);
      }
    );
  });
}
