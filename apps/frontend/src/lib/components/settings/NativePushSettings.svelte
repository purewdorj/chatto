<!--
@component

Turns native push notifications on or off in the iOS and Android apps and
lets the user send a test notification through the current server.

Push is on for every signed-in server or for none of them. Checking the box
asks the operating system for notification permission when it has not
decided yet. The panel shows a notice instead of the controls when the
server cannot deliver to this platform or the system blocks notifications.
Render it only when `nativePush.available` is true.
-->
<script lang="ts">
  import { Code, ConnectError } from '@connectrpc/connect';
  import { m } from '$lib/i18n/messages';
  import { nativePush } from '$lib/notifications/nativePushNotifications.svelte';
  import { useServerScope } from '$lib/state/server/scope.svelte';
  import { Hint, Panel } from '$lib/ui';
  import { Button, Checkbox } from '$lib/ui/form';

  type TestResult = { kind: 'sent' } | { kind: 'failed'; message: string };

  const serverScope = useServerScope();
  const serverId = serverScope.serverId;
  const store = serverScope.store;

  const serverSupportsPlatform = $derived(
    nativePush.platform !== null &&
      store.serverInfo.nativePushPlatforms.includes(nativePush.platform)
  );
  const on = $derived(nativePush.on);
  /** Follows `on`; the checkbox overrides it until its change finishes. */
  let checked = $derived(on);
  const registered = $derived(nativePush.isRegistered(serverId, store.accountId));
  const failure = $derived(on && !registered ? nativePush.failure(serverId) : null);

  let changing = $state(false);
  let turnOffFailed = $state(false);
  let retrying = $state(false);
  let testing = $state(false);
  let testResult = $state<TestResult | null>(null);

  const error = $derived.by(() => {
    if (turnOffFailed) return m('settings.notifications.push.turn_off_failed');
    if (failure) return m('settings.notifications.push.setup_failed', { reason: failure });
    return undefined;
  });
  const description = $derived(
    on && !registered && !failure
      ? m('settings.notifications.push.registering')
      : m('settings.notifications.push.description', { device: nativePush.deviceName })
  );

  $effect(() => {
    void nativePush.refreshPermission();
  });

  async function change(event: Event): Promise<void> {
    const turnOn = (event.currentTarget as HTMLInputElement).checked;
    changing = true;
    turnOffFailed = false;
    try {
      if (turnOn) await nativePush.enable();
      else await nativePush.disable();
    } catch (cause) {
      console.error('Failed to change push notifications on this device:', cause);
      if (!turnOn) turnOffFailed = true;
    } finally {
      changing = false;
      checked = on;
    }
  }

  async function retry(): Promise<void> {
    retrying = true;
    try {
      if (turnOffFailed) {
        turnOffFailed = false;
        await nativePush.disable();
      } else {
        await nativePush.retry(serverId);
      }
    } catch (cause) {
      console.error('Failed to retry push notifications on this device:', cause);
      turnOffFailed = true;
    } finally {
      retrying = false;
    }
  }

  async function sendTest(): Promise<void> {
    testing = true;
    testResult = null;
    try {
      await nativePush.sendTest(serverId);
      testResult = { kind: 'sent' };
    } catch (cause) {
      const rateLimited = ConnectError.from(cause).code === Code.ResourceExhausted;
      testResult = {
        kind: 'failed',
        message: rateLimited
          ? m('settings.notifications.push.test_rate_limited')
          : m('settings.notifications.push.test_failed')
      };
    } finally {
      testing = false;
    }
  }
</script>

<Panel title={m('settings.notifications.push.title')} icon="iconify icon-[uil--bell]">
  <div class="flex max-w-2xl flex-col gap-4" data-testid="native-push-settings">
    {#if !serverSupportsPlatform}
      <Hint>{m('settings.notifications.push.server_unconfigured')}</Hint>
    {:else if nativePush.permission === 'denied'}
      <Hint tone="warning" icon="icon-[uil--bell-slash]">
        <p class="font-medium">{m('settings.notifications.push.blocked_title')}</p>
        <p>{m('settings.notifications.push.blocked_description')}</p>
      </Hint>
    {:else}
      <Checkbox
        id="native-push-on-this-device"
        label={m('settings.notifications.push.label')}
        {description}
        {error}
        loading={changing}
        bind:checked
        onchange={change}
      />
      {#if turnOffFailed || failure}
        <div>
          <Button
            variant="secondary"
            size="sm"
            onclick={retry}
            disabled={retrying}
            loading={retrying}
          >
            {m('common.retry')}
          </Button>
        </div>
      {:else if on && registered}
        <div class="flex flex-wrap items-center gap-3">
          <Button
            size="sm"
            onclick={sendTest}
            disabled={testing}
            loading={testing}
            loadingText={m('settings.notifications.push.testing')}
          >
            {m('settings.notifications.push.test_button')}
          </Button>
          {#if testResult?.kind === 'sent'}
            <p class="text-success" role="status">{m('settings.notifications.push.test_sent')}</p>
          {:else if testResult?.kind === 'failed'}
            <p class="text-danger" role="alert">{testResult.message}</p>
          {/if}
        </div>
      {/if}
    {/if}
  </div>
</Panel>
