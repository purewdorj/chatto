import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';
import { serverRegistry } from '$lib/client';
import { serverIdToSegment } from '$lib/navigation';
import { ensureSetunariServer } from '$lib/serverCatalogue';

export const load: PageLoad = async ({ parent, url }) => {
  const { user } = await parent();
  if (user) redirect(302, `/chat${url.search}`);
  if (serverRegistry.originServer) redirect(302, '/login');
  // Setunari: the desktop app has no origin server; start it with the company server.
  const serverId = await ensureSetunariServer();
  redirect(302, serverId ? `/chat/${serverIdToSegment(serverId)}` : '/chat/servers');
};
