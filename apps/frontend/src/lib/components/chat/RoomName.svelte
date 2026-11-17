<!--
@component

A channel room name with its channel emoji, hash, or universal-room globe. The name wraps and
isolates user-authored text from the surrounding reading direction.
-->
<script lang="ts">
  import { m } from '$lib/i18n/messages';
  import { roomIcon } from '$lib/render/roomIcon';

  let {
    name,
    isUniversal = false,
    emoji = ''
  }: { name: string; isUniversal?: boolean; emoji?: string } = $props();
  const icon = $derived(roomIcon({ isUniversal, emoji }));
  const labelUniversal = $derived(isUniversal && !emoji);
</script>

<span class="inline-flex min-w-0 items-baseline gap-2">
  <span
    class={['shrink-0 text-muted', labelUniversal && 'self-center', icon.className]}
    role={labelUniversal ? 'img' : undefined}
    aria-label={labelUniversal ? m('room.create.universal_label') : undefined}
    aria-hidden={labelUniversal ? undefined : true}>{icon.text}</span
  >
  <bdi class="min-w-0 break-words">{name}</bdi>
</span>
