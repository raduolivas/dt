import { describe, expect, it } from 'vitest';
import { applyTheme, theme } from './theme';

describe('theme', () => {
  it('has the brief tokens', () => {
    expect(theme).toMatchObject({ accent: '#22d3ee', ok: '#4ade80', warn: '#f59e0b', fail: '#ef4444', sim: '#a78bfa' });
    expect(theme.mono).toContain('monospace');
  });
  it('publishes CSS variables', () => {
    applyTheme();
    expect(document.documentElement.style.getPropertyValue('--mc-panel-edge')).toBe(theme.panelEdge);
    expect(document.documentElement.style.getPropertyValue('--mc-accent')).toBe('#22d3ee');
  });
});
