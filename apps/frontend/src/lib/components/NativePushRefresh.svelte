<!--
@component

Keeps native push registrations current in the iOS and Android apps. This
component has no visible UI and never requests notification permission; the
user turns push on in notification settings.

It opens the conversation of a tapped notification, including the one that
launched the app. On iOS it also saves the PushKit VoIP token, which lets
CallKit ring calls while the app is closed, and saves again when it changes.
After permission is granted, it registers this device with every eligible
server, also with a server that becomes eligible later. When
the app returns to the foreground and once an hour, it reads the permission
again and saves registrations that are a day old or failed.

Include this component once in the chat root. It does nothing in a browser.
-->
<script lang="ts">
  import Interval from '$lib/lifecycle/Interval.svelte';
  import { nativePush } from '$lib/notifications/nativePushNotifications.svelte';

  const refreshCheckIntervalMs = 60 * 60 * 1000;

  nativePush.listenForOpenedNotifications();
  nativePush.listenForVoipToken();

  $effect(() => {
    const targets = nativePush.targets();
    if (!nativePush.on || targets.length === 0) return;

    void nativePush.refresh();
  });

  async function refreshAfterResume(): Promise<void> {
    if (!nativePush.available || document.visibilityState !== 'visible') return;
    await nativePush.refreshPermission();
    await nativePush.refresh();
  }
</script>

<svelte:document onvisibilitychange={() => void refreshAfterResume()} />
{#if nativePush.available}
  <Interval milliseconds={refreshCheckIntervalMs} ontick={() => void nativePush.refresh()} />
{/if}
