import { Html, useCursor } from '@react-three/drei';
import { useFrame, type ThreeEvent } from '@react-three/fiber';
import { memo, useEffect, useMemo, useRef, useState } from 'react';
import * as THREE from 'three';
import type { ComponentWithState } from '../api/types';
import { theme } from '../theme';
import type { Vec3 } from './camera';
import { CUBE_H, CUBE_W } from './layout';
import { STATE_LOOK, type VisualState } from './visualState';

/** Opacity factor for parts an order did not touch (spec 6.2, brief: 35 %). */
export const DIM = 0.35;

const BODY = '#0c131c';
/** How lit the top plate is per state: lit when active, a low glow when idle, none when grey. */
const TOP_GLOW: Record<VisualState, number> = {
  active: 0.32, idle: 0.12, warning: 0.3, unhealthy: 0.34, not_instrumented: 0, stale: 0, not_built: 0,
};

let hatch: THREE.CanvasTexture | null = null;
/** Diagonal stripes for "stale / no data", drawn on a canvas (no data: URL, no image request). */
function hatchTexture(): THREE.CanvasTexture {
  if (hatch) return hatch;
  const c = document.createElement('canvas');
  c.width = c.height = 64;
  const g = c.getContext('2d');
  if (g) {
    g.strokeStyle = '#9aa7b4';
    g.lineWidth = 6;
    for (let i = -64; i < 128; i += 16) {
      g.beginPath();
      g.moveTo(i, 64);
      g.lineTo(i + 64, 0);
      g.stroke();
    }
  }
  hatch = new THREE.CanvasTexture(c);
  hatch.wrapS = hatch.wrapT = THREE.RepeatWrapping;
  hatch.repeat.set(2, 2);
  return hatch;
}

export interface CubeProps {
  comp: ComponentWithState;
  position: Vec3;
  state: VisualState;
  caption: string[];
  dim: boolean;
  selected: boolean;
  reduced: boolean;
  onSelect(id: string): void;
}

/** A labelled server-like cube whose outline, LED and label show its state (spec 6.3). */
export const ComponentCube = memo(function ComponentCube({ comp, position, state, caption, dim, selected, reduced, onSelect }: CubeProps) {
  const look = STATE_LOOK[state];
  const k = dim ? DIM : 1;
  const [hover, setHover] = useState(false);
  useCursor(hover);
  const led = useRef<THREE.MeshBasicMaterial>(null);

  const edges = useMemo(() => new THREE.EdgesGeometry(new THREE.BoxGeometry(CUBE_W, CUBE_H, CUBE_W)), []);
  const outline = useMemo(() => {
    if (look.dash === 'dashed') return new THREE.LineDashedMaterial({ color: look.color, dashSize: 0.16, gapSize: 0.11, transparent: true });
    if (look.dash === 'dotted') return new THREE.LineDashedMaterial({ color: look.color, dashSize: 0.04, gapSize: 0.1, transparent: true });
    return new THREE.LineBasicMaterial({ color: look.color, transparent: true });
  }, [look.color, look.dash]);
  outline.opacity = look.opacity * k;
  useEffect(() => () => edges.dispose(), [edges]);
  useEffect(() => () => outline.dispose(), [outline]);
  const segs = useRef<THREE.LineSegments>(null);
  const setSegs = (l: THREE.LineSegments | null) => {
    segs.current = l;
    l?.computeLineDistances(); // dashed materials need distances
  };

  // Active: the LED breathes. Idle: lit and still. Reduced motion: still.
  useFrame(({ clock }) => {
    if (!led.current) return;
    const base = state === 'active' ? 0.95 : state === 'stale' || state === 'not_built' ? 0.25 : 0.8;
    const breathe = state === 'active' && !reduced ? 0.55 + 0.45 * Math.sin(clock.elapsedTime * 3.2) : 1;
    led.current.opacity = base * breathe * k;
  });

  const click = (e: ThreeEvent<MouseEvent>) => {
    e.stopPropagation();
    onSelect(comp.id);
  };
  const ghost = state === 'not_built';
  const bodyOpacity = (ghost ? 0.25 : state === 'stale' ? 0.6 : 0.96) * k;

  return (
    <group position={position}>
      <mesh
        onClick={click}
        onPointerOver={(e) => (e.stopPropagation(), setHover(true))}
        onPointerOut={() => setHover(false)}
      >
        <boxGeometry args={[CUBE_W, CUBE_H, CUBE_W]} />
        <meshStandardMaterial color={BODY} metalness={0.35} roughness={0.55} transparent opacity={bodyOpacity} depthWrite={!ghost} />
      </mesh>
      {/* rack slots on the two faces the camera sees */}
      {!ghost
        ? [-0.22, 0.06, 0.34].map((y) => (
            <group key={y}>
              <mesh position={[0, y - CUBE_H / 2 + 0.3, CUBE_W / 2 + 0.002]}>
                <planeGeometry args={[CUBE_W * 0.78, 0.05]} />
                <meshBasicMaterial color="#1c2a38" transparent opacity={k} />
              </mesh>
              <mesh position={[CUBE_W / 2 + 0.002, y - CUBE_H / 2 + 0.3, 0]} rotation={[0, Math.PI / 2, 0]}>
                <planeGeometry args={[CUBE_W * 0.78, 0.05]} />
                <meshBasicMaterial color="#1c2a38" transparent opacity={k} />
              </mesh>
            </group>
          ))
        : null}
      {/* status LED strip, front face */}
      <mesh position={[0, CUBE_H / 2 - 0.18, CUBE_W / 2 + 0.004]}>
        <planeGeometry args={[CUBE_W * 0.78, 0.07]} />
        <meshBasicMaterial ref={led} color={look.color} transparent toneMapped={false} />
      </mesh>
      {TOP_GLOW[state] > 0 ? (
        <mesh position={[0, CUBE_H / 2 + 0.002, 0]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[CUBE_W * 0.92, CUBE_W * 0.92]} />
          <meshBasicMaterial color={look.color} transparent opacity={TOP_GLOW[state] * k} toneMapped={false} depthWrite={false} />
        </mesh>
      ) : null}
      {state === 'stale' ? (
        <mesh position={[0, CUBE_H / 2 + 0.003, 0]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[CUBE_W, CUBE_W]} />
          <meshBasicMaterial map={hatchTexture()} transparent opacity={0.28 * k} depthWrite={false} />
        </mesh>
      ) : null}
      <lineSegments ref={setSegs} geometry={edges} material={outline} />
      {selected ? (
        <lineSegments geometry={edges} scale={1.14}>
          <lineBasicMaterial color={theme.accent} transparent opacity={0.9} toneMapped={false} />
        </lineSegments>
      ) : null}
      <Html position={[0, CUBE_H / 2 + 1.3, 0]} center zIndexRange={[20, 10]}>
        <div className={`mc-label is-${state}${dim ? ' is-dim' : ''}${selected ? ' is-selected' : ''}`}>
          <span className="mc-label-name">{comp.label}</span>
          {caption.map((l) => (
            <span key={l} className="mc-label-state">{l}</span>
          ))}
        </div>
      </Html>
    </group>
  );
});
