import { describe, it, expect } from 'vitest';
import { MAX_POSITION_LENGTH, normalizePosition, validateAndNormalizePosition } from './position';

describe('normalizePosition', () => {
  it('trims and joins whitespace runs into single spaces, like the server', () => {
    expect(normalizePosition('  Senior\n  Sales\tManager  ')).toBe('Senior Sales Manager');
    expect(normalizePosition('   ')).toBe('');
  });
});

describe('validateAndNormalizePosition', () => {
  it('accepts a position at the length limit, counted in Unicode characters', () => {
    expect(validateAndNormalizePosition('職'.repeat(MAX_POSITION_LENGTH))).toEqual({
      valid: true,
      normalized: '職'.repeat(MAX_POSITION_LENGTH)
    });
  });

  it('rejects a position over the length limit', () => {
    const result = validateAndNormalizePosition('x'.repeat(MAX_POSITION_LENGTH + 1));
    expect(result.valid).toBe(false);
    expect(result.error).toBeTruthy();
  });

  it('accepts an empty position, which clears it', () => {
    expect(validateAndNormalizePosition('  ')).toEqual({ valid: true, normalized: '' });
  });
});
