import { useFrame, type ThreeEvent } from '@react-three/fiber';
import { useMemo, useRef, useState, type RefObject } from 'react';
import * as THREE from 'three';
import { store } from '../state/store';
import { pulseSelection } from './linking';
import type { SceneModel } from './model';
import { theme } from '../theme';
import { DIM } from './ComponentCube';
import type { FrameItem, PulseEngine } from './engine';
import { PULSE_HEX, pulseColor } from './visualState';

export interface PulsesProps {
  engine: PulseEngine;
  model: SceneModel;
  curves: ReadonlyMap<string, THREE.QuadraticBezierCurve3>;
  reduced: boolean;
  /** true when the wire is outside the searched order (drawn at 35 %). */
  isDim(wire: string): boolean;
  /** A DOM layer over the canvas for count labels (bundles, reduced-motion markers). */
  overlayRef: RefObject<HTMLDivElement | null>;
}

const START_CAPACITY = 256;
const LABEL_LIFT = 0.45;

/**
 * All pulses in two instanced meshes (core and halo), updated in useFrame from
 * the engine. Count labels are a pool of spans in a DOM overlay, positioned in
 * the same frame (CSSOM transforms, text via textContent). Nothing in React
 * renders per pulse or per frame.
 */
export function Pulses({ engine, model, curves, reduced, isDim, overlayRef }: PulsesProps) {
  const [cap, setCap] = useState(START_CAPACITY);
  const core = useRef<THREE.InstancedMesh>(null);
  const halo = useRef<THREE.InstancedMesh>(null);
  const shown = useRef<FrameItem[]>([]);
  const tmp = useMemo(
    () => ({
      m: new THREE.Matrix4(), p: new THREE.Vector3(), q: new THREE.Quaternion(), s: new THREE.Vector3(),
      c: new THREE.Color(), sim: new THREE.Color(theme.sim), v: new THREE.Vector3(),
    }),
    [],
  );

  useFrame(({ camera, size }) => {
    const c = core.current;
    const h = halo.current;
    if (!c || !h) return;
    const now = Date.now();
    const st = store.getState();
    // Replay (R34): static markers for the 5 s before the cursor, nothing travels; the live stream is closed meanwhile.
    const replaying = st.mode === 'replay' && st.replayAt !== null;
    if (!replaying) engine.sync(st, now);
    const layer = overlayRef.current;
    let n = 0;
    let labels = 0;
    let overflow = false;
    const sel = st.selection?.kind === 'event' ? Number(st.selection.id) : null;
    let selSeen = false;
    const items: FrameItem[] = [];
    const visit = (it: FrameItem) => {
      const curve = curves.get(it.wire);
      if (!curve) return;
      if (n >= cap) {
        overflow = true;
        return;
      }
      curve.getPoint(it.t, tmp.p);
      const dim = isDim(it.wire);
      const k = dim ? DIM : 1;
      const hl = sel !== null && it.eventId === sel;
      if (hl) selSeen = true;
      const scale = (it.static ? 1.35 : it.bundle ? 1.7 : 1) * (hl ? 1.8 : 1);
      tmp.m.compose(tmp.p, tmp.q, tmp.s.setScalar(scale));
      c.setMatrixAt(n, tmp.m);
      c.setColorAt(n, tmp.c.set(hl ? '#ffffff' : PULSE_HEX[it.color]).multiplyScalar(k));
      tmp.m.compose(tmp.p, tmp.q, tmp.s.setScalar(scale * 2.3));
      h.setMatrixAt(n, tmp.m);
      // simulated pulses keep their outcome colour at the core and wear a purple halo
      h.setColorAt(n, (it.simulated ? tmp.c.copy(tmp.sim) : tmp.c.set(PULSE_HEX[it.color])).multiplyScalar(k));
      items[n] = it;
      n++;
      if (layer && (it.bundle || it.static)) {
        tmp.v.set(tmp.p.x, tmp.p.y + LABEL_LIFT, tmp.p.z).project(camera);
        const el = (layer.children[labels] as HTMLSpanElement | undefined) ?? layer.appendChild(document.createElement('span'));
        const text = it.static ? `${it.count}` : `×${it.count}`;
        if (el.textContent !== text) el.textContent = text;
        const cls = `mc-count is-${it.color}${dim ? ' is-dim' : ''}`;
        if (el.className !== cls) el.className = cls;
        el.style.transform = `translate(${((tmp.v.x + 1) / 2) * size.width}px, ${((1 - tmp.v.y) / 2) * size.height}px) translate(-50%, -50%)`;
        el.style.display = '';
        labels++;
      }
    };
    if (replaying) engine.replayMarkers(st.events, st.replayAt as number).forEach(visit);
    else engine.each(now, reduced, visit);
    // The selected event's pulse is gone (or never travelled): keep it as a static marker on its wire, so a log line can still be seen on the scene.
    if (sel !== null && !selSeen) {
      const e = st.events.find((x) => x.id === sel);
      const p = e ? model.placeEvent(e) : null;
      if (e && p) visit({ key: `sel:${sel}`, wire: p.wire, t: 0.5, color: pulseColor(e.outcome), simulated: e.origin === 'simulated', count: 1, bundle: false, static: false, eventId: sel });
    }
    shown.current = items;
    c.boundingSphere = null; // instances move every frame: raycasts must not use a stale sphere
    h.boundingSphere = null;
    if (layer) for (let i = labels; i < layer.children.length; i++) (layer.children[i] as HTMLElement).style.display = 'none';
    c.count = n;
    h.count = n;
    c.instanceMatrix.needsUpdate = true;
    h.instanceMatrix.needsUpdate = true;
    if (c.instanceColor) c.instanceColor.needsUpdate = true;
    if (h.instanceColor) h.instanceColor.needsUpdate = true;
    if (overflow) setCap((x) => x * 2); // never hide an observed pulse: grow instead
  });

  const click = (e: ThreeEvent<MouseEvent>) => {
    const it = e.instanceId === undefined ? undefined : shown.current[e.instanceId];
    if (!it) return;
    const s = pulseSelection(it);
    if (!s) return;
    e.stopPropagation();
    store.select(s);
  };

  return (
    <group>
      <instancedMesh key={`c${cap}`} ref={core} args={[undefined, undefined, cap]} frustumCulled={false} onClick={click}>
        <sphereGeometry args={[0.13, 14, 10]} />
        <instancedBufferAttribute attach="instanceColor" args={[new Float32Array(cap * 3), 3]} />
        <meshBasicMaterial toneMapped={false} />
      </instancedMesh>
      <instancedMesh key={`h${cap}`} ref={halo} args={[undefined, undefined, cap]} frustumCulled={false} onClick={click}>
        <sphereGeometry args={[0.13, 12, 8]} />
        <instancedBufferAttribute attach="instanceColor" args={[new Float32Array(cap * 3), 3]} />
        <meshBasicMaterial toneMapped={false} transparent opacity={0.22} blending={THREE.AdditiveBlending} depthWrite={false} />
      </instancedMesh>
    </group>
  );
}
