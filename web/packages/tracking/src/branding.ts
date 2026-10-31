// Partner color handling: the primary color tints the cards, but text keeps WCAG AA contrast (4.5:1).

type Rgb = [number, number, number];

function parseHex(hex: string): Rgb {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) throw new Error(`not a #RRGGBB color: ${hex}`);
  const n = parseInt(m[1]!, 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

const toHex = (rgb: Rgb) => '#' + rgb.map((c) => Math.round(c).toString(16).padStart(2, '0')).join('');

/** WCAG relative luminance. */
function luminance(hex: string): number {
  const [r, g, b] = parseHex(hex).map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  }) as Rgb;
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio between two `#RRGGBB` colors (1 to 21). */
export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

/** Black or white, whichever reads better on `bg`. */
export function readableOn(bg: string): '#000000' | '#ffffff' {
  return contrastRatio('#000000', bg) >= contrastRatio('#ffffff', bg) ? '#000000' : '#ffffff';
}

function toHsl([r, g, b]: Rgb): Rgb {
  const [rr, gg, bb] = [r / 255, g / 255, b / 255];
  const max = Math.max(rr, gg, bb);
  const min = Math.min(rr, gg, bb);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  const h = max === rr ? (gg - bb) / d + (gg < bb ? 6 : 0) : max === gg ? (bb - rr) / d + 2 : (rr - gg) / d + 4;
  return [h / 6, s, l];
}

function fromHsl([h, s, l]: Rgb): Rgb {
  if (s === 0) return [l * 255, l * 255, l * 255];
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const p = 2 * l - q;
  const ch = (t: number) => {
    const u = t < 0 ? t + 1 : t > 1 ? t - 1 : t;
    if (u < 1 / 6) return p + (q - p) * 6 * u;
    if (u < 1 / 2) return q;
    if (u < 2 / 3) return p + (q - p) * (2 / 3 - u) * 6;
    return p;
  };
  return [ch(h + 1 / 3) * 255, ch(h) * 255, ch(h - 1 / 3) * 255];
}

/**
 * `color` as it may be used for text and accents on `bg`: unchanged when it already has `min` contrast, else darkened
 * in HSL (same hue and saturation) until it passes. On a dark background darkening can never pass, so it lightens.
 */
export function ensureContrast(color: string, bg: string, min = 4.5): string {
  const hex = toHex(parseHex(color));
  if (contrastRatio(hex, bg) >= min) return hex;
  const [h, s, l0] = toHsl(parseHex(hex));
  const step = readableOn(bg) === '#000000' ? -0.01 : 0.01;
  for (let l = l0 + step; l >= 0 && l <= 1; l += step) {
    const c = toHex(fromHsl([h, s, l]));
    if (contrastRatio(c, bg) >= min) return c;
  }
  return readableOn(bg);
}
