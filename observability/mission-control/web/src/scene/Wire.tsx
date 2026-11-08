import { Line, useCursor } from '@react-three/drei';
import type { ThreeEvent } from '@react-three/fiber';
import { memo, useMemo, useState } from 'react';
import * as THREE from 'three';
import { theme } from '../theme';
import { DIM } from './ComponentCube';
import type { SceneWire } from './model';
import { WIRE_LOOK, type WireLook } from './visualState';

export interface WireProps {
  wire: SceneWire;
  curve: THREE.QuadraticBezierCurve3;
  look: WireLook;
  dim: boolean;
  selected: boolean;
  onSelect(id: string): void;
}

/** A glowing arc: a thin core over a wide faint halo. Inferred links are dotted (spec 6.3). */
export const Wire = memo(function Wire({ wire, curve, look, dim, selected, onSelect }: WireProps) {
  const points = useMemo(() => curve.getPoints(48), [curve]);
  const [hover, setHover] = useState(false);
  useCursor(hover);
  const l = WIRE_LOOK[look];
  const k = dim ? DIM : 1;
  const color = selected ? '#a5f3fc' : l.color;
  const dotted = l.dash === 'dotted';
  const click = (e: ThreeEvent<MouseEvent>) => {
    e.stopPropagation();
    onSelect(wire.id);
  };
  return (
    <group>
      {look !== 'quiet' ? (
        <Line points={points} color={selected ? theme.accent : l.color} lineWidth={7} transparent opacity={0.1 * k} depthWrite={false} toneMapped={false} />
      ) : null}
      <Line
        points={points}
        color={color}
        lineWidth={selected || hover ? 2.6 : 1.6}
        dashed={dotted}
        dashSize={0.07}
        gapSize={0.2}
        transparent
        opacity={(selected ? 1 : l.opacity) * k}
        toneMapped={false}
        onClick={click}
        onPointerOver={(e) => (e.stopPropagation(), setHover(true))}
        onPointerOut={() => setHover(false)}
      />
    </group>
  );
});
