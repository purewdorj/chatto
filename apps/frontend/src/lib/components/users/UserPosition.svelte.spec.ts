import { describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { createRawSnippet } from 'svelte';
import UserCard from '$lib/ui/UserCard.svelte';
import UserPosition from './UserPosition.svelte';

const livePositions = vi.hoisted(() => new Map<string, string | null>());

vi.mock('$lib/state/userProfiles.svelte', () => ({
  getLivePosition: (userId: string, fallback: string | null) =>
    livePositions.has(userId) ? livePositions.get(userId)! : fallback
}));

const avatar = createRawSnippet(() => ({ render: () => '<span></span>' }));

describe('UserPosition', () => {
  it('renders the live position before the row fallback', async () => {
    livePositions.set('U1', 'Head of Sales');
    const screen = render(UserPosition, { userId: 'U1', position: 'Sales Manager' });

    await expect.element(screen.getByTestId('user-position')).toHaveTextContent('Head of Sales');
    livePositions.clear();
  });

  it('renders the fallback when no live profile is loaded', async () => {
    const screen = render(UserPosition, { userId: 'U2', position: 'Sales Manager' });

    await expect.element(screen.getByTestId('user-position')).toHaveTextContent('Sales Manager');
  });

  it('renders nothing when the user has no position', () => {
    const { container } = render(UserPosition, { userId: 'U3', position: null });

    expect(container.querySelector('[data-testid="user-position"]')).toBeNull();
  });
});

describe('UserCard position', () => {
  it('shows the position before the username', async () => {
    const screen = render(UserCard, {
      name: 'Ada',
      username: 'ada',
      position: 'Sales Manager',
      avatar,
      secondaryTestId: 'secondary'
    });

    await expect
      .element(screen.getByTestId('secondary'))
      .toHaveTextContent(/^Sales Manager\s·\s@ada$/);
  });

  it('shows only the username without a position', async () => {
    const screen = render(UserCard, {
      name: 'Ada',
      username: 'ada',
      avatar,
      secondaryTestId: 'secondary'
    });

    await expect.element(screen.getByTestId('secondary')).toHaveTextContent('@ada');
    expect(screen.container.querySelector('[data-testid="user-position"]')).toBeNull();
  });
});
