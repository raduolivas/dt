// Visual language (spec 6.5): dark background, restrained neon, monospace for data.
export const theme = {
  accent: '#22d3ee', // cyan: interface accent, never a pulse colour
  ok: '#4ade80', // green: succeeded
  warn: '#f59e0b', // amber: slow, retried, dropped, rejected
  fail: '#ef4444', // red: failed
  unknown: '#94a3b8', // neutral grey: outcome not known (R32), never green
  sim: '#a78bfa', // purple: simulated
  bg: '#0b0f14',
  panel: '#111821',
  panelEdge: '#1f2a37',
  text: '#d6e2ee',
  muted: '#7b8a9a',
  mono: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
} as const;

export type Theme = typeof theme;

/**
 * Publishes the tokens as CSS custom properties on the root element. Set
 * through the CSSOM, which `default-src 'self'` allows (no inline style
 * attribute or style tag in the HTML).
 */
export function applyTheme(root: HTMLElement = document.documentElement): void {
  for (const [k, v] of Object.entries(theme)) {
    root.style.setProperty(`--mc-${k.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())}`, v);
  }
}
