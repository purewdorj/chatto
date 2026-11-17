/**
 * Profile position (job title) validation matching the backend rules.
 *
 * A position is one line of plain text. The server joins whitespace runs into
 * single spaces, trims the result, and limits it to 64 Unicode characters.
 * An empty position clears it.
 */

import { m } from '$lib/i18n/messages';
import type { ValidationResult } from './displayName';

/** Maximum position length in Unicode characters (matching backend). */
export const MAX_POSITION_LENGTH = 64;

/** Normalize a position draft the same way the server does before it stores it. */
export function normalizePosition(position: string): string {
  return position.trim().split(/\s+/u).filter(Boolean).join(' ');
}

/** Validate and normalize a position draft. Returns the normalized value if valid. */
export function validateAndNormalizePosition(
  position: string
): ValidationResult & { normalized?: string } {
  const normalized = normalizePosition(position);
  if ([...normalized].length > MAX_POSITION_LENGTH) {
    return {
      valid: false,
      error: m('settings.profile.position.too_long', { max: MAX_POSITION_LENGTH })
    };
  }
  return { valid: true, normalized };
}
