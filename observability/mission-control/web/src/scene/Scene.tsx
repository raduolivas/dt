import { OrbitControls } from '@react-three/drei';
import { Canvas, useFrame, useThree, type RootState } from '@react-three/fiber';
import { Component as ReactComponent, useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react';
import * as THREE from 'three';
import { api } from '../api/client';
import type { ComponentWithState, TopologyResponse } from '../api/types';
import { store, useStore } from '../state/store';
import { theme } from '../theme';
import { CAMERA_DISTANCE, MAX_ZOOM, MIN_ZOOM, fitZoom, isoPosition, tween, type CameraPose, type Vec3 } from './camera';
import { ComponentCube } from './ComponentCube';
import { eventsAt } from '../order/replay';
import { Breadcrumb } from './Expanded';
import { buildExpanded, EXPAND_ORIGIN } from './expand';
import { highlightWire } from './linking';
import { PulseEngine } from './engine';
import { layoutScene, wireArc, type Layout } from './layout';
import { buildModel, derive, type Derived, type SceneModel } from './model';
import { Platform } from './Platform';
import { Pulses } from './Pulses';
import { PULSE_HEX, STATE_CAPTION, STATE_LOOK, VISUAL_STATES, WIRE_LOOK, caption, stateFrom, wireStyle } from './visualState';
import { Wire } from './Wire';

const TOPOLOGY_EVERY_MS = 10_000;
const DERIVE_EVERY_MS = 1000;

// ---- data -------------------------------------------------------------------

/** /api/topology, polled: the structure rarely changes, the states do. */
function useTopology(): { topo: TopologyResponse | null; error: string | null } {
  const [topo, setTopo] = useState<TopologyResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      try {
        const t = await api.topology();
        if (!alive) return;
        setTopo(t);
        setError(null);
      } catch (e) {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      }
      if (alive) timer = setTimeout(load, TOPOLOGY_EVERY_MS);
    };
    void load();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, []);
  return { topo, error };
}

/** Activity, wire evidence and touched parts, recomputed at most once a second from the buffer (not per event). */
function useDerived(model: SceneModel): { derived: Derived; now: number } {
  const [tick, setTick] = useState(() => ({ now: Date.now(), events: store.getState().events }));
  useEffect(() => {
    const id = setInterval(() => setTick({ now: Date.now(), events: store.getState().events }), DERIVE_EVERY_MS);
    return () => clearInterval(id);
  }, []);
  // Replay: only what had happened by the cursor, and "now" is the cursor.
  const cursor = useStore((s) => (s.mode === 'replay' ? s.replayAt : null));
  const events = useMemo(() => (cursor === null ? tick.events : eventsAt(tick.events, cursor)), [tick.events, cursor]);
  const derived = useMemo(() => derive(model, events), [model, events]);
  return { derived, now: cursor ?? tick.now };
}

function structureKey(t: TopologyResponse): string {
  return JSON.stringify([t.components.map((c) => [c.id, c.parent, c.zone, c.label, c.instrumented, c.status]), t.wires.map((w) => [w.id, w.channel])]);
}

// ---- camera -------------------------------------------------------------------

export interface RigApi {
  reset(): void;
}

/** Fits the isometric view to the platforms; orbit/pan/zoom via OrbitControls; reset tweens home (instant with reduced motion). */
interface Controls {
  target: THREE.Vector3;
  update(): void;
}
const asControls = (c: unknown): Controls | null =>
  c && typeof c === 'object' && 'target' in c && c.target instanceof THREE.Vector3 ? (c as Controls) : null;

/** Moves the default camera (read from the fiber store, not captured at render) and the controls' target. */
function applyPose(get: () => RootState, p: CameraPose) {
  const { camera } = get();
  const controls = asControls(get().controls);
  camera.position.set(...p.position);
  camera.zoom = p.zoom;
  camera.updateProjectionMatrix();
  if (controls) {
    controls.target.set(...p.target);
    controls.update();
  } else {
    camera.lookAt(...p.target);
  }
}

export interface Focus {
  center: Vec3;
  bounds: Layout['bounds'];
}

function CameraRig({ layout, focus, reduced, rigRef }: { layout: Layout; focus: Focus | null; reduced: boolean; rigRef: RefObject<RigApi | null> }) {
  const get = useThree((s) => s.get);
  const width = useThree((s) => s.size.width);
  const height = useThree((s) => s.size.height);
  const movedRef = useRef(false);
  const animRef = useRef<{ from: CameraPose; to: CameraPose; t0: number } | null>(null);

  const home = useCallback(
    (): CameraPose => ({ position: isoPosition([0, 0, 0], CAMERA_DISTANCE), target: [0, 0, 0], zoom: fitZoom(layout.bounds, width, height) }),
    [layout, width, height],
  );

  // Where the camera belongs: home, or the expanded component's platform.
  const goal = useCallback(
    (): CameraPose =>
      focus ? { position: isoPosition(focus.center, CAMERA_DISTANCE), target: focus.center, zoom: fitZoom(focus.bounds, width, height) } : home(),
    [focus, home, width, height],
  );
  const goalRef = useRef(goal);
  useEffect(() => {
    goalRef.current = goal;
  });

  // Follow the viewport until the user moves the camera.
  useEffect(() => {
    if (!movedRef.current) applyPose(get, goalRef.current());
  }, [get, home]);

  // Expanding or going back tweens to the new goal (no tween under reduced motion: useFrame jumps).
  const prevFocus = useRef<Focus | null>(null);
  useEffect(() => {
    if (prevFocus.current === focus) return;
    prevFocus.current = focus;
    movedRef.current = false;
    const { camera } = get();
    const c = asControls(get().controls);
    const target = (c ? c.target.toArray() : [0, 0, 0]) as Vec3;
    animRef.current = { from: { position: camera.position.toArray() as Vec3, target, zoom: camera.zoom }, to: goalRef.current(), t0: performance.now() };
  }, [focus, get]);

  useEffect(() => {
    rigRef.current = {
      reset() {
        movedRef.current = false;
        const { camera } = get();
        const c = asControls(get().controls);
        const target = (c ? c.target.toArray() : [0, 0, 0]) as Vec3;
        animRef.current = { from: { position: camera.position.toArray() as Vec3, target, zoom: camera.zoom }, to: home(), t0: performance.now() };
      },
    };
    return () => {
      rigRef.current = null;
    };
  }, [get, home, rigRef]);

  useFrame(() => {
    const a = animRef.current;
    if (!a) return;
    const { pose, done } = tween(a.from, a.to, performance.now() - a.t0, reduced);
    applyPose(get, pose);
    if (done) animRef.current = null;
  });

  return (
    <OrbitControls
      makeDefault
      enableDamping={!reduced}
      dampingFactor={0.12}
      screenSpacePanning
      minZoom={MIN_ZOOM}
      maxZoom={MAX_ZOOM}
      minPolarAngle={0.15}
      maxPolarAngle={1.32}
      onStart={() => {
        movedRef.current = true;
        animRef.current = null;
      }}
    />
  );
}

// ---- world --------------------------------------------------------------------

interface WorldProps {
  model: SceneModel;
  layout: Layout;
  states: Map<string, ComponentWithState>;
  reduced: boolean;
  overlayRef: RefObject<HTMLDivElement | null>;
  /** Parts of this component can be opened (main view only). */
  canExpand?: (id: string) => boolean;
}

function World({ model, layout, states, reduced, overlayRef, canExpand }: WorldProps) {
  const orderId = useStore((s) => s.orderId);
  const selection = useStore((s) => s.selection);
  const { derived, now } = useDerived(model);

  const curves = useMemo(() => {
    const m = new Map<string, THREE.QuadraticBezierCurve3>();
    for (const w of model.wires) {
      const a = layout.cubes.get(w.a);
      const b = layout.cubes.get(w.b);
      if (!a || !b) continue;
      const [s, c, e] = wireArc(a, b);
      m.set(w.id, new THREE.QuadraticBezierCurve3(new THREE.Vector3(...s), new THREE.Vector3(...c), new THREE.Vector3(...e)));
    }
    return m;
  }, [model, layout]);

  const engine = useMemo(() => {
    const lengths = new Map([...curves].map(([id, c]) => [id, c.getLength()]));
    return new PulseEngine(model, (id) => lengths.get(id) ?? 8);
  }, [model, curves]);
  useEffect(() => engine.clear(), [engine, orderId]);

  // With an order searched, what it did not touch dims to 35 %.
  const touched = derived.touched;
  const isDim = useCallback((id: string) => orderId !== null && !touched.has(id), [orderId, touched]);
  const selectComponent = useCallback(
    (id: string) => {
      store.select({ kind: 'component', id });
      if (canExpand?.(id)) store.expand(id);
    },
    [canExpand],
  );
  // The selected event's wire is highlighted (logs <-> pulses, spec 6.4).
  const selectedEvent = useStore((s) => (s.selection?.kind === 'event' ? (s.events.find((e) => String(e.id) === s.selection?.id) ?? null) : null));
  const hlWire = useMemo(
    () => highlightWire(model, selectedEvent ? [selectedEvent] : [], selectedEvent ? { kind: 'event', id: String(selectedEvent.id) } : null),
    [model, selectedEvent],
  );
  const selectWire = useCallback((id: string) => store.select({ kind: 'wire', id }), []);

  return (
    <>
      {layout.platforms.map((p) => (
        <Platform key={p.zone} p={p} />
      ))}
      {model.wires.map((w) => {
        const c = curves.get(w.id);
        return c ? (
          <Wire
            key={w.id}
            wire={w}
            curve={c}
            look={wireStyle(w, derived.evidence)}
            dim={isDim(w.id)}
            selected={(selection?.kind === 'wire' && selection.id === w.id) || hlWire === w.id}
            onSelect={selectWire}
          />
        ) : null;
      })}
      {model.cubes.map((base) => {
        const comp = states.get(base.id) ?? base;
        const pos = layout.cubes.get(base.id);
        if (!pos) return null;
        const last = derived.activity.get(base.id) ?? null;
        const st = stateFrom(comp, last, now);
        return (
          <ComponentCube
            key={base.id}
            comp={comp}
            position={pos}
            state={st}
            caption={caption(comp, st, last, now)}
            dim={isDim(base.id)}
            selected={selection?.kind === 'component' && selection.id === base.id}
            reduced={reduced}
            onSelect={selectComponent}
          />
        );
      })}
      <Pulses engine={engine} model={model} curves={curves} reduced={reduced} isDim={isDim} overlayRef={overlayRef} />
    </>
  );
}

// ---- shell --------------------------------------------------------------------

class SceneBoundary extends ReactComponent<{ children: ReactNode }, { failed: string | null }> {
  state = { failed: null as string | null };
  static getDerivedStateFromError(e: unknown) {
    return { failed: e instanceof Error ? e.message : String(e) };
  }
  render() {
    if (this.state.failed !== null) {
      return (
        <p className="mc-scene-msg" role="alert">
          The 3D scene could not start (WebGL unavailable?): {this.state.failed}
        </p>
      );
    }
    return this.props.children;
  }
}

function Legend() {
  const states = VISUAL_STATES.map((st) => [st, STATE_CAPTION[st]] as const);
  return (
    <div className="mc-legend" aria-label="Legend">
      <div className="mc-legend-row">
        <span className="mc-dot is-ok" /> ok <span className="mc-dot is-warn" /> slow · retried · dropped · rejected{' '}
        <span className="mc-dot is-fail" /> failed <span className="mc-dot is-unknown" /> unknown{' '}
        <span className="mc-dot is-sim" /> simulated
      </div>
      <div className="mc-legend-row">
        <span className="mc-line is-measured" /> traced <span className="mc-line is-inferred" /> linked by ids, not trace{' '}
        <span className="mc-line is-quiet" /> no traffic
      </div>
      <div className="mc-legend-row">
        {states.map(([st, name]) => (
          <span key={st} className={`mc-swatch is-${st} is-${STATE_LOOK[st].dash}`}>
            {name}
          </span>
        ))}
      </div>
    </div>
  );
}

/** The centre of the cockpit: isometric zones scene (spec 6.2, 6.3, 6.5). */
export function Scene() {
  const { topo, error } = useTopology();
  const reduced = useStore((s) => s.reducedMotion);
  const rig = useRef<RigApi | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const overlay = useRef<HTMLDivElement>(null);
  const overlay2 = useRef<HTMLDivElement>(null);
  const expandedId = useStore((s) => s.expanded);
  // Follow the OS setting when it changes; the top-bar toggle still overrides it.
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    const on = (e: MediaQueryListEvent) => store.setReducedMotion(e.matches);
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  }, []);
  // The look tokens as CSS custom properties (CSSOM, allowed by default-src 'self'), so labels and legend match the 3D colours.
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    for (const st of VISUAL_STATES) el.style.setProperty(`--mc-st-${st}`, STATE_LOOK[st].color);
    for (const [k, v] of Object.entries(PULSE_HEX)) el.style.setProperty(`--mc-pulse-${k}`, v);
    for (const [k, v] of Object.entries(WIRE_LOOK)) el.style.setProperty(`--mc-wire-${k}`, v.color);
  }, []);

  const key = topo ? structureKey(topo) : '';
  // Rebuild the model (and the pulse engine) only when the structure changes, not on every state poll.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const model = useMemo(() => (topo ? buildModel(topo) : null), [key]);
  const layout = useMemo(() => (model ? layoutScene(model.cubes) : null), [model]);
  const states = useMemo(() => new Map((topo?.components ?? []).map((c) => [c.id, c])), [topo]);
  const parents = useMemo(() => new Set((topo?.components ?? []).map((c) => c.parent).filter(Boolean)), [topo]);
  const canExpand = useCallback((id: string) => parents.has(id), [parents]);
  // The expanded component's working parts, rebuilt only when the structure changes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const view = useMemo(() => (topo && expandedId ? buildExpanded(topo, expandedId) : null), [key, expandedId]);
  const focus = useMemo<Focus | null>(() => (view ? { center: EXPAND_ORIGIN, bounds: view.layout.bounds } : null), [view]);

  return (
    <div className="mc-scene" ref={root}>
      {model && layout ? (
        <SceneBoundary>
          <Canvas
            orthographic
            dpr={[1, 2]}
            camera={{ position: isoPosition([0, 0, 0], CAMERA_DISTANCE), zoom: 20, near: 0.1, far: 500 }}
            gl={{ antialias: true }}
            onPointerMissed={() => store.select(null)}
            aria-label="System scene"
          >
            <color attach="background" args={[theme.bg]} />
            <ambientLight intensity={0.55} />
            <hemisphereLight args={['#9be7f5', '#05080c', 0.35]} />
            <directionalLight position={[12, 26, 16]} intensity={1.1} />
            <gridHelper args={[160, 80, '#122030', '#0e1822']} position={[0, -0.47, 0]} />
            <CameraRig layout={layout} focus={focus} reduced={reduced} rigRef={rig} />
            <World model={model} layout={layout} states={states} reduced={reduced} overlayRef={overlay} canExpand={canExpand} />
            {view ? <World key={view.component.id} model={view.model} layout={view.layout} states={states} reduced={reduced} overlayRef={overlay2} /> : null}
          </Canvas>
        </SceneBoundary>
      ) : (
        <p className="mc-scene-msg" role="status">
          {error ? `Topology unavailable: ${error}. Retrying.` : 'Loading topology…'}
        </p>
      )}
      <div className="mc-pulse-labels" ref={overlay} aria-hidden="true" />
      <div className="mc-pulse-labels" ref={overlay2} aria-hidden="true" />
      <Breadcrumb label={(id) => states.get(id)?.label ?? id} />
      {model && error ? (
        <p className="mc-scene-banner" role="status">
          Topology refresh failed: {error}. States may be out of date.
        </p>
      ) : null}
      <div className="mc-scene-hud">
        <button
          type="button"
          onClick={() => {
            store.expand(null);
            rig.current?.reset();
          }}
          aria-label="Reset view"
        >
          Reset view
        </button>
      </div>
      <Legend />
    </div>
  );
}
