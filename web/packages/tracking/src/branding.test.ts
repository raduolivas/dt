import { describe, expect, it } from 'vitest';
import { contrastRatio, ensureContrast, readableOn } from './branding';

describe('readableOn', () => {
  it('picks black on light colors and white on dark ones', () => {
    expect(readableOn('#ffeb3b')).toBe('#000000');
    expect(readableOn('#ffffff')).toBe('#000000');
    expect(readableOn('#1a237e')).toBe('#ffffff');
    expect(readableOn('#2f5bea')).toBe('#ffffff');
  });
});

describe('ensureContrast', () => {
  it('darkens a yellow until it reads on white (AA)', () => {
    const c = ensureContrast('#ffeb3b', '#ffffff');
    expect(c).toMatch(/^#[0-9a-f]{6}$/);
    expect(contrastRatio(c, '#ffffff')).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps a color that already passes', () => {
    expect(ensureContrast('#1a237e', '#ffffff')).toBe('#1a237e');
  });

  it('honours a custom minimum', () => {
    expect(contrastRatio(ensureContrast('#ffeb3b', '#ffffff', 3), '#ffffff')).toBeGreaterThanOrEqual(3);
  });

  it('lightens instead on a dark card (darkening can never pass there)', () => {
    const c = ensureContrast('#1a237e', '#141a30');
    expect(contrastRatio(c, '#141a30')).toBeGreaterThanOrEqual(4.5);
  });
});

describe('contrastRatio', () => {
  it('is 21 for black on white', () => {
    expect(contrastRatio('#000000', '#ffffff')).toBeCloseTo(21, 5);
  });
});
