<!--
@component

Shows a user's public position (job title) as muted plain text next to or below
their name. Follows live profile updates when a user profile store is
available, and falls back to `position` otherwise. Renders nothing when the
user has no position.

**Props:**
- `userId` - User whose live position to read
- `position` - Fallback position from the row that renders the user
- `class` - Optional classes for size and layout on the owning surface
-->
<script lang="ts">
  import type { ClassValue } from 'svelte/elements';
  import { getLivePosition } from '$lib/state/userProfiles.svelte';

  let {
    userId,
    position = null,
    class: className
  }: {
    userId: string;
    position?: string | null;
    class?: ClassValue;
  } = $props();

  const livePosition = $derived(getLivePosition(userId, position));
</script>

{#if livePosition}
  <span
    class={['min-w-0 truncate font-normal text-muted', className]}
    data-testid="user-position"
    title={livePosition}><bdi>{livePosition}</bdi></span
  >
{/if}
