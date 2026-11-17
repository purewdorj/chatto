import { describe, expect, it } from 'vitest';
import { RoomKind } from '@chatto/client/api/roomDirectory';
import { roomIcon } from './roomIcon';

describe('roomIcon', () => {
  it('uses the channel symbol without an emoji', () => {
    expect(roomIcon({ type: RoomKind.CHANNEL })).toEqual({ className: '', text: '#' });
  });

  it('shows the channel emoji in place of the symbol and the universal globe', () => {
    expect(roomIcon({ type: RoomKind.CHANNEL, emoji: '🚀' })).toEqual({
      className: '',
      text: '🚀'
    });
    expect(roomIcon({ type: RoomKind.CHANNEL, isUniversal: true, emoji: '🚀' }).text).toBe('🚀');
  });

  it('ignores an emoji for direct messages', () => {
    expect(roomIcon({ type: RoomKind.DM, emoji: '🚀' }).text).toBe('');
  });
});
