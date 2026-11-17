import { goto } from '$app/navigation';
import type { ServerStateStore } from '@chatto/client/server/store';
import { conversationPath } from './conversationPath';
import { getRoomSidebarPresentation, type AppUiState } from '$lib/state/appUi.svelte';
import { getVoiceCallJoinErrorMessage } from '$lib/state/server/voiceCall.svelte';
import { serverUi } from '$lib/state/server/serverUi';
import { toast } from '$lib/ui/toast';

/**
 * Answers an incoming direct-message call: stops its ring, opens the
 * conversation with the call panel, and joins the call. The in-app banner and
 * the native call UI of the mobile apps both answer through this function.
 */
export async function joinIncomingCall(
  appUi: AppUiState,
  serverId: string,
  store: ServerStateStore,
  roomId: string
): Promise<void> {
  const ui = serverUi(store);
  ui.incomingCall.clear();
  appUi.requestRoomSidebarPanel(serverId, roomId, 'call', getRoomSidebarPresentation());
  await goto(conversationPath(serverId, roomId, { store }));
  const livekitUrl = store.serverInfo.livekitUrl;
  if (!livekitUrl) return;
  try {
    await ui.voiceCall.join(livekitUrl, roomId);
  } catch (err) {
    store.handleVoiceCallJoinFailed(roomId);
    toast.error(getVoiceCallJoinErrorMessage(err));
  }
}
