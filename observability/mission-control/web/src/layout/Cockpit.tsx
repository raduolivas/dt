import type { ReactNode } from 'react';
import { Search } from '../order/Search';
import { store, useStore, type Mode, type PanelName } from '../state/store';

export interface CockpitProps {
  left?: ReactNode;
  center?: ReactNode;
  right?: ReactNode;
  bottom?: ReactNode;
}

const MODES: { mode: Mode; label: string }[] = [
  { mode: 'live', label: 'Live' },
  { mode: 'paused', label: 'Pause' },
  { mode: 'replay', label: 'Replay' },
];

const TITLES: Record<PanelName, string> = { left: 'left panel', right: 'right panel', bottom: 'timeline' };

function Toggle({ panel }: { panel: PanelName }) {
  const collapsed = useStore((s) => s.collapsed[panel]);
  return (
    <button
      type="button"
      className="mc-toggle"
      aria-expanded={!collapsed}
      aria-label={`${collapsed ? 'Expand' : 'Collapse'} ${TITLES[panel]}`}
      onClick={() => store.togglePanel(panel)}
    >
      {collapsed ? '+' : '–'}
    </button>
  );
}

function Panel({ panel, className, children }: { panel: PanelName; className: string; children?: ReactNode }) {
  const collapsed = useStore((s) => s.collapsed[panel]);
  return (
    <section className={`mc-panel ${className}${collapsed ? ' is-collapsed' : ''}`} aria-label={TITLES[panel]}>
      <Toggle panel={panel} />
      {collapsed ? null : <div className="mc-panel-body">{children}</div>}
    </section>
  );
}

function TopBar() {
  const mode = useStore((s) => s.mode);
  const warnings = useStore((s) => s.findings.length);
  const simulated = useStore((s) => s.events.some((e) => e.origin === 'simulated'));
  const allCollapsed = useStore((s) => s.collapsed.left && s.collapsed.right && s.collapsed.bottom);
  const reduced = useStore((s) => s.reducedMotion);
  return (
    <header className="mc-topbar">
      <strong className="mc-brand">Mission Control</strong>
      <div role="group" aria-label="Mode" className="mc-modes">
        {MODES.map((m) => (
          <button
            key={m.mode}
            type="button"
            aria-pressed={mode === m.mode}
            className={mode === m.mode ? 'is-on' : ''}
            onClick={() => store.setMode(m.mode)}
          >
            {m.label}
          </button>
        ))}
      </div>
      <Search />
      {simulated ? <span className="mc-badge mc-badge-sim">SIMULATED</span> : null}
      <span className={`mc-badge${warnings > 0 ? ' mc-badge-warn' : ''}`}>
        {warnings} {warnings === 1 ? 'warning' : 'warnings'}
      </span>
      <button type="button" aria-pressed={reduced} onClick={() => store.setReducedMotion(!reduced)}>
        Reduced motion
      </button>
      <button type="button" aria-pressed={allCollapsed} onClick={() => store.setPanels(!allCollapsed)}>
        Full scene
      </button>
    </header>
  );
}

/** Cockpit layout (MC-7, MC-7a): top bar, left panel, centre, right panel, bottom timeline; the panels collapse. */
export function Cockpit({ left, center, right, bottom }: CockpitProps) {
  return (
    <div className="mc-cockpit">
      <TopBar />
      <Panel panel="left" className="mc-left">{left}</Panel>
      <main className="mc-center">{center}</main>
      <Panel panel="right" className="mc-right">{right}</Panel>
      <Panel panel="bottom" className="mc-bottom">{bottom}</Panel>
    </div>
  );
}
