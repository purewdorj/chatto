<!--
@component

Connects the native call UI of the mobile apps to the web call. When the user
answers a call that iOS CallKit or the Android call notification rang, the
app opens the conversation and joins the call. It waits until the app is in
front, because the web view can use the microphone only then, and until the
server session has loaded. A decline stops the in-app ring of that call.

Answers older than {@link ANSWER_LIFETIME_MS} are dropped, so a late answer
cannot start a new call after the caller has left. Include this component
once in the chat layout. It does nothing in a browser.
-->
<script lang="ts" module>
  /** How long an answer may wait for the app and the server before it is dropped. */
  export const ANSWER_LIFETIME_MS = 60_000;
</script>

<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { serverRegistry } from '$lib/client';
  import {
    deliverPendingNativeAnswers,
    endNativeCall,
    hasNativeCalls,
    onNativeCallActions,
    type NativeCallInfo
  } from '$lib/desktop/nativeCalls';
  import { findServerByUrl } from '$lib/serverCatalogue';
  import { getAppUiState } from '$lib/state/appUi.svelte';
  import { serverUi } from '$lib/state/server/serverUi';
  import { joinIncomingCall } from '$lib/navigation/joinIncomingCall';

  type PendingAnswer = NativeCallInfo & { receivedAt: number };

  const appUi = getAppUiState();

  let pending = $state<PendingAnswer | null>(null);
  let visible = $state(typeof document === 'undefined' || document.visibilityState === 'visible');

  /** Answers already handled. A native side can deliver one answer twice. */
  const handledCallIds = new SvelteSet<string>();

  function answer(call: NativeCallInfo): void {
    if (handledCallIds.has(call.callId)) return;
    handledCallIds.add(call.callId);
    pending = { ...call, receivedAt: Date.now() };
  }

  function onVisibilityChange(): void {
    visible = document.visibilityState === 'visible';
    if (visible) void deliverPendingNativeAnswers(answer);
  }

  function storeFor(serverOrigin: string) {
    const server = findServerByUrl(serverOrigin);
    return server ? { serverId: server.id, store: serverRegistry.tryGetStore(server.id) } : null;
  }

  $effect(() => {
    if (!hasNativeCalls()) return;
    let disposed = false;
    let remove: (() => void) | null = null;
    void onNativeCallActions({
      answered: answer,
      declined: (call) => {
        const target = storeFor(call.serverOrigin);
        if (!target?.store) return;
        const incoming = serverUi(target.store).incomingCall;
        if (incoming.ringing?.roomId === call.roomId) incoming.clear();
      }
    }).then((dispose) => {
      if (disposed) dispose();
      else remove = dispose;
    });
    return () => {
      disposed = true;
      remove?.();
    };
  });

  // Join once the app is in front and the server can join calls.
  $effect(() => {
    const answer = pending;
    if (!answer || !visible) return;
    if (Date.now() - answer.receivedAt > ANSWER_LIFETIME_MS) {
      pending = null;
      endNativeCall(answer.callId);
      return;
    }
    const target = storeFor(answer.serverOrigin);
    if (!target?.store) {
      pending = null;
      endNativeCall(answer.callId);
      return;
    }
    const { serverId, store } = target;
    if (!store.isAuthenticated || !store.serverInfo.livekitUrl) return;
    pending = null;
    void joinIncomingCall(appUi, serverId, store, answer.roomId).finally(() =>
      endNativeCall(answer.callId)
    );
  });
</script>

<svelte:document onvisibilitychange={onVisibilityChange} />
