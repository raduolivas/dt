import { Html } from '@react-three/drei';
import { memo, useEffect, useMemo } from 'react';
import * as THREE from 'three';
import { theme } from '../theme';
import { PLATFORM_H, ZONE_LABEL, type PlatformLayout } from './layout';

/** A dark raised platform for one zone, with a faint cyan rim and its name on the front edge. */
export const Platform = memo(function Platform({ p }: { p: PlatformLayout }) {
  const [w, d] = p.size;
  const edges = useMemo(() => new THREE.EdgesGeometry(new THREE.BoxGeometry(w, PLATFORM_H, d)), [w, d]);
  useEffect(() => () => edges.dispose(), [edges]);
  return (
    <group position={p.center}>
      <mesh receiveShadow>
        <boxGeometry args={[w, PLATFORM_H, d]} />
        <meshStandardMaterial color="#0d141d" metalness={0.2} roughness={0.85} />
      </mesh>
      {/* inset top plate */}
      <mesh position={[0, PLATFORM_H / 2 + 0.002, 0]} rotation={[-Math.PI / 2, 0, 0]}>
        <planeGeometry args={[w - 0.5, d - 0.5]} />
        <meshStandardMaterial color="#111b26" metalness={0.1} roughness={0.9} />
      </mesh>
      <lineSegments geometry={edges}>
        <lineBasicMaterial color={theme.accent} transparent opacity={0.22} />
      </lineSegments>
      <Html position={[-w / 2 + 0.3, PLATFORM_H / 2, d / 2 + 0.2]} zIndexRange={[5, 0]} className="mc-zone-label">
        {ZONE_LABEL[p.zone]}
      </Html>
    </group>
  );
});
