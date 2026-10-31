// The SDK's CSS, inside its Shadow DOM: the partner page's CSS cannot reach it and none of it leaks out. The look
// follows the renderer dev page's HUD (glass cards, soft shadows, blue accent); a partner color replaces the accent.
// The renderer's own CSS (scene labels, map credit) is included here, since document styles do not cross the shadow.
import sceneCss from '@dt/renderer/scene.css?inline';
import { GUTTER, LINE_HEIGHT, SHEET_MAX } from './layout';

/** Card backgrounds as solid colors, for the contrast checks of a partner color. */
export const CARD_BG = { light: '#ffffff', dark: '#141a30' } as const;

const css = /* css */ `
:host {
  all: initial; display: block; position: relative; width: 100%; height: 100%; min-height: 320px; contain: content;
}
.dt-root {
  --bg: #e4eaf7; --card: rgba(255, 255, 255, 0.94); --edge: rgba(255, 255, 255, 0.7);
  --ink: #141b34; --muted: #5d6787; --line: #e3e8f5; --line-strong: #cfd7ec;
  --accent: #2f5bea; --on-accent: #ffffff; --done: #1fb67a;
  --shadow: 0 10px 30px rgba(32, 52, 110, 0.14), 0 2px 6px rgba(32, 52, 110, 0.08);
  --r: 18px; --g: ${GUTTER}px; --side-w: 360px;
  position: absolute; inset: 0; overflow: hidden; background: var(--bg); color: var(--ink);
  font: 400 14px/1.4 system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
  -webkit-font-smoothing: antialiased; -webkit-tap-highlight-color: transparent; text-align: left;
}
.dt-root[data-theme='dark'] {
  --bg: #0b1024; --card: rgba(20, 26, 48, 0.92); --edge: rgba(255, 255, 255, 0.08);
  --ink: #eef2ff; --muted: #a3add0; --line: rgba(255, 255, 255, 0.09); --line-strong: rgba(255, 255, 255, 0.22);
  --accent: #8ea8ff; --on-accent: #0b1024; --done: #37c887;
  --shadow: 0 10px 30px rgba(0, 0, 0, 0.45), 0 2px 6px rgba(0, 0, 0, 0.3);
}
*, *::before, *::after { box-sizing: border-box; }
button { font: inherit; color: inherit; cursor: pointer; }
.dt-sr { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }

.dt-map { position: absolute; inset: 0; overflow: hidden; touch-action: none; }

/* glass surfaces */
.dt-panel, .dt-mode {
  background: var(--card); border: 1px solid var(--edge); box-shadow: var(--shadow);
  -webkit-backdrop-filter: blur(14px); backdrop-filter: blur(14px);
}
.dt-mode {
  position: absolute; z-index: 3; top: max(var(--g), env(safe-area-inset-top)); right: max(var(--g), env(safe-area-inset-right));
  min-width: 44px; height: 44px; padding: 0 12px; border-radius: 14px;
  font-weight: 800; font-size: 13px; letter-spacing: 0.02em;
}
.dt-mode:hover { color: var(--accent); }
.dt-mode:focus-visible, .dt-handle:focus-visible { outline: 3px solid var(--accent); outline-offset: 2px; }

/* the panel: bottom sheet, one line, or side panel */
.dt-panel {
  position: absolute; z-index: 2; display: flex; flex-direction: column; overflow: hidden; border-radius: var(--r);
}
.dt-root[data-layout='sheet'] .dt-panel {
  left: max(var(--g), env(safe-area-inset-left)); right: max(var(--g), env(safe-area-inset-right));
  bottom: max(var(--g), env(safe-area-inset-bottom)); max-height: ${SHEET_MAX * 100}%; max-width: 560px; margin: 0 auto;
}
.dt-root[data-layout='line'] .dt-panel {
  left: max(var(--g), env(safe-area-inset-left)); right: max(var(--g), env(safe-area-inset-right));
  bottom: max(var(--g), env(safe-area-inset-bottom)); height: ${LINE_HEIGHT}px; max-width: 560px; margin: 0 auto;
  border-radius: 999px;
}
.dt-root[data-layout='side'] .dt-panel {
  top: max(var(--g), env(safe-area-inset-top)); bottom: max(var(--g), env(safe-area-inset-bottom));
  left: max(var(--g), env(safe-area-inset-left)); width: var(--side-w); max-height: none;
}
.dt-root[data-layout='side'] .dt-panel:not([data-collapsible]) .dt-handle { display: none; }
.dt-root[data-layout='line'] .dt-cards { display: none; }
.dt-root[data-layout='sheet'] .dt-panel[data-expanded='false'] :is([data-card='route'], [data-card='timeline']) { display: none; }

.dt-handle {
  flex: none; display: flex; align-items: center; justify-content: center; gap: 10px; width: 100%;
  min-height: 28px; padding: 10px 16px 2px; border: 0; background: transparent; color: var(--muted);
}
.dt-grip { width: 38px; height: 5px; border-radius: 3px; background: var(--line-strong); }
.dt-line-text, .dt-chevron { display: none; }
.dt-root[data-layout='line'] .dt-handle { height: 100%; padding: 0 18px; justify-content: space-between; }
.dt-root[data-layout='line'] .dt-grip { display: none; }
.dt-root[data-layout='line'] .dt-line-text {
  display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-weight: 800; font-size: 14px; color: var(--ink);
}
.dt-root[data-layout='line'] .dt-chevron { display: block; flex: none; color: var(--accent); }

.dt-cards {
  display: flex; flex-direction: column; padding: 2px 18px 14px; overflow-y: auto; overscroll-behavior: contain;
  -webkit-overflow-scrolling: touch;
}
.dt-card { padding: 12px 0; border-top: 1px solid var(--line); animation: dt-in 0.25s ease both; }
.dt-card:first-child { border-top: 0; }
.dt-card p, .dt-card h2, .dt-card ol { margin: 0; }
@keyframes dt-in { from { opacity: 0; transform: translateY(4px); } }

.dt-card-brand { display: flex; align-items: center; gap: 10px; padding: 8px 0 10px; }
.dt-logo { width: 32px; height: 32px; border-radius: 9px; object-fit: contain; background: #fff; flex: none; }
.dt-brand-name { font-weight: 800; font-size: 15px; letter-spacing: -0.01em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.dt-head { font-size: 19px; font-weight: 800; letter-spacing: -0.02em; line-height: 1.2; }
.dt-sub { margin-top: 2px !important; font-size: 13px; color: var(--muted); }
.dt-eta { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; margin-top: 10px !important; }
.dt-eta-label { font-size: 12px; font-weight: 700; color: var(--muted); }
.dt-eta-value { font-size: 30px; font-weight: 800; letter-spacing: -0.03em; line-height: 1.1; font-variant-numeric: tabular-nums; }
.dt-eta-calc .dt-eta-value { font-size: 15px; font-weight: 700; letter-spacing: 0; color: var(--muted); }
.dt-badge {
  align-self: center; font-size: 11px; font-weight: 800; color: var(--accent); padding: 3px 9px; border-radius: 999px;
  background: rgba(47, 91, 234, 0.1); background: color-mix(in srgb, var(--accent) 13%, transparent);
}

.dt-card-courier { display: flex; align-items: center; gap: 12px; }
.dt-avatar {
  display: grid; place-items: center; flex: none; width: 42px; height: 42px; border-radius: 50%;
  background: var(--accent); color: var(--on-accent);
}
.dt-courier { display: flex; flex-direction: column; min-width: 0; }
.dt-name { font-size: 15px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dt-meta { font-size: 13px; color: var(--muted); }

.dt-route { list-style: none; padding: 0; display: grid; gap: 10px; position: relative; }
.dt-route::before {
  content: ''; position: absolute; left: 15px; top: 30px; bottom: 30px; border-left: 2px dotted var(--line-strong);
}
.dt-stop { display: grid; grid-template-columns: 32px 1fr; column-gap: 10px; align-items: center; }
.dt-stop-icon {
  grid-row: span 2; display: grid; place-items: center; width: 32px; height: 32px; border-radius: 10px;
  background: rgba(47, 91, 234, 0.1); background: color-mix(in srgb, var(--accent) 12%, transparent); color: var(--accent);
}
.dt-stop-icon svg { width: 18px; height: 18px; }
.dt-stop-home .dt-stop-icon { color: #e0532f; background: rgba(255, 107, 74, 0.14); }
.dt-root[data-theme='dark'] .dt-stop-home .dt-stop-icon { color: #ff8a6e; }
.dt-stop-label { font-size: 11px; font-weight: 700; color: var(--muted); line-height: 1.2; }
.dt-stop b { font-size: 14px; line-height: 1.25; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.dt-timeline { list-style: none; padding: 0; }
.dt-step { position: relative; display: flex; justify-content: space-between; gap: 12px; padding: 5px 0 5px 24px; font-size: 13px; }
.dt-step::before {
  content: ''; position: absolute; left: 3px; top: 9px; width: 10px; height: 10px; border-radius: 50%; z-index: 1;
  background: var(--accent); border: 2px solid var(--accent);
}
.dt-step::after { content: ''; position: absolute; left: 7px; top: 19px; width: 2px; height: calc(100% - 4px); background: var(--accent); }
.dt-step:last-child::after { display: none; }
.dt-step-current { font-weight: 800; }
.dt-step-current::before {
  background: var(--card); box-shadow: 0 0 0 4px rgba(47, 91, 234, 0.18);
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--accent) 22%, transparent);
}
.dt-step time { color: var(--muted); font-variant-numeric: tabular-nums; font-weight: 600; }

.dt-card-final { display: grid; grid-template-columns: 46px 1fr; column-gap: 12px; align-items: center; padding-top: 14px; }
.dt-card-final .dt-final-icon { grid-row: span 2; }
.dt-final-icon {
  display: grid; place-items: center; width: 46px; height: 46px; border-radius: 50%; color: var(--accent);
  background: rgba(47, 91, 234, 0.1); background: color-mix(in srgb, var(--accent) 13%, transparent);
}
.dt-final-delivered .dt-final-icon { background: var(--done); color: #fff; }

.dt-card-warming, .dt-card-inactive { display: flex; align-items: center; gap: 12px; padding: 16px 0; }
.dt-card-warming .dt-head, .dt-card-inactive .dt-head { font-size: 16px; }
.dt-card-inactive .dt-final-icon { flex: none; }
.dt-spinner {
  flex: none; width: 24px; height: 24px; border-radius: 50%; border: 3px solid var(--line-strong);
  border-top-color: var(--accent); animation: dt-spin 0.9s linear infinite;
}
@keyframes dt-spin { to { transform: rotate(360deg); } }
.dt-card-loading { display: grid; gap: 10px; padding: 16px 0; }
.dt-skel {
  display: block; height: 18px; border-radius: 8px; background: var(--line);
  animation: dt-pulse 1.2s ease-in-out infinite alternate;
}
.dt-skel-short { width: 55%; height: 30px; }
@keyframes dt-pulse { to { opacity: 0.45; } }

@media (prefers-reduced-motion: reduce) {
  .dt-root *, .dt-root *::before, .dt-root *::after { animation: none !important; transition: none !important; }
}
`;

/** Everything the shadow root needs. */
export const STYLES = css + sceneCss;
