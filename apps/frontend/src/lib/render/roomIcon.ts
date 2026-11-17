/** Shared room-kind icons for names, navigation, links, and composer suggestions. */
import { RoomKind } from '@chatto/client/api/roomDirectory';

/**
 * Return the room's icon classes and fallback text. A channel emoji replaces
 * the channel symbol. Otherwise, ordinary channels use the text hash,
 * universal channels use a globe, and group DMs use speech bubbles.
 * Callers own sizing, accessible labels, and membership/action indicators.
 */
export function roomIcon(room: { type?: RoomKind; isUniversal?: boolean; emoji?: string }): {
  className: string;
  text: string;
} {
  if (room.type === RoomKind.DM) return { className: 'iconify icon-[uil--comments]', text: '' };
  if (room.emoji) return { className: '', text: room.emoji };
  if (room.isUniversal) return { className: 'iconify icon-[uil--globe]', text: '' };
  return { className: '', text: '#' };
}
