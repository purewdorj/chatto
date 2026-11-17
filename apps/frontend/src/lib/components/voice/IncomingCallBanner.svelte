<!--
  @component
  Rings and shows a banner for an incoming direct-message call on any
  signed-in server. The ringtone plays while the banner shows, except in Do
  Not Disturb. When the browser blocks the ringtone, or the tab is in the
  background, a system notification also signals the call, if the user
  granted notification permission. **Join** opens the conversation with the call panel and joins
  the call. **Decline** stops the ring on this device only; the call
  continues for the caller.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { serverRegistry } from '$lib/client';
  import UserAvatar from '$lib/components/UserAvatar.svelte';
  import { Button } from '$lib/ui/form';
  import { canPlayRingtone, startRingtone } from '$lib/audio/ringtone';
  import { m } from '$lib/i18n/messages';
  import { conversationPath } from '$lib/navigation/conversationPath';
  import { showIncomingCallNotification } from '$lib/notifications/incomingCallNotification';
  import { notificationAlertsMuted } from '$lib/notifications/notificationAlerts';
  import { getAppUiState, getRoomSidebarPresentation } from '$lib/state/appUi.svelte';
  import { getVoiceCallJoinErrorMessage } from '$lib/state/server/voiceCall.svelte';
  import { serverUi } from '$lib/state/server/serverUi';
  import { toast } from '$lib/ui/toast';

  const appUi = getAppUiState();

  /** The first call that rings on a signed-in server, with its caller. */
  const incoming = $derived.by(() => {
    for (const server of serverRegistry.servers) {
      const store = serverRegistry.tryGetStore(server.id);
      if (!store?.isAuthenticated) continue;
      const ui = serverUi(store);
      const call = ui.incomingCall.ringing;
      if (!call) continue;
      const room = store.roomList.rooms.find((candidate) => candidate.id === call.roomId);
      const caller = room?.members.find((member) => member.id === call.callerId) ?? null;
      return { serverId: server.id, store, ui, call, caller };
    }
    return null;
  });

  const muted = $derived(
    incoming ? notificationAlertsMuted(incoming.serverId, incoming.store) : true
  );

  /** Changes only for a different call, so room list updates do not restart the ring. */
  const ringKey = $derived(
    incoming ? `${incoming.serverId}:${incoming.call.roomId}:${incoming.call.callId}` : null
  );

  const callerName = $derived(incoming?.caller?.displayName ?? m('voice.incoming_call'));

  // Ring only while a call rings and the viewer accepts interruptions.
  $effect(() => {
    if (!ringKey || muted) return;
    const stopRingtone = startRingtone();
    const closeNotification = untrack(() =>
      document.visibilityState !== 'visible' || !canPlayRingtone()
        ? showIncomingCallNotification({ title: m('voice.incoming_call'), body: callerName })
        : () => {}
    );
    return () => {
      stopRingtone();
      closeNotification();
    };
  });

  async function join(): Promise<void> {
    if (!incoming) return;
    const { serverId, store, ui, call } = incoming;
    ui.incomingCall.clear();
    appUi.requestRoomSidebarPanel(serverId, call.roomId, 'call', getRoomSidebarPresentation());
    await goto(conversationPath(serverId, call.roomId, { store }));
    const livekitUrl = store.serverInfo.livekitUrl;
    if (!livekitUrl) return;
    try {
      await ui.voiceCall.join(livekitUrl, call.roomId);
    } catch (err) {
      store.handleVoiceCallJoinFailed(call.roomId);
      toast.error(getVoiceCallJoinErrorMessage(err));
    }
  }

  function decline(): void {
    incoming?.ui.incomingCall.clear();
  }
</script>

{#if incoming}
  <div
    class="pointer-events-none fixed start-3 end-3 top-[calc(env(safe-area-inset-top,0px)+0.75rem)] z-50 flex justify-center"
    role="alertdialog"
    aria-live="assertive"
    aria-label={m('voice.incoming_call')}
    data-testid="incoming-call-banner"
  >
    <div class="pointer-events-auto w-full max-w-96 min-w-0 menu text-start">
      <div class="flex items-center gap-3 menu-section px-3 py-2">
        {#if incoming.caller}
          <UserAvatar user={incoming.caller} size="sm" />
        {:else}
          <span
            class="iconify icon-[uil--phone] size-5 shrink-0 animate-pulse text-action"
            aria-hidden="true"
          ></span>
        {/if}
        <div class="flex min-w-0 flex-1 flex-col leading-snug">
          <span class="truncate font-medium"><bdi>{callerName}</bdi></span>
          <span class="truncate text-muted">{m('voice.incoming_call')}</span>
        </div>
        <Button variant="secondary" size="xs" onclick={decline}>
          {m('voice.decline_call')}
        </Button>
        <Button size="xs" onclick={join}>
          <span class="iconify icon-[uil--phone] size-4" aria-hidden="true"></span>
          {m('voice.join_call')}
        </Button>
      </div>
    </div>
  </div>
{/if}
