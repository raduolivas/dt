import type { Vec2 } from './geo';
import { BLOCK, GRID } from './world';

function dedupe(points: Vec2[]): Vec2[] {
  return points.filter(
    (p, i) => i === 0 || Math.hypot(p.x - points[i - 1].x, p.z - points[i - 1].z) > 0.01,
  );
}

/**
 * Manhattan route between two curb points that sit on horizontal streets:
 * ride along the street to a crossing, along the avenue, then along the target street.
 * `prefer` picks which end the crossing is closest to when several are equally short,
 * so the two legs of a delivery use different corners.
 */
export function routeBetween(a: Vec2, b: Vec2, prefer: 'a' | 'b'): Vec2[] {
  const rowA = Math.round(a.z / BLOCK);
  const rowB = Math.round(b.z / BLOCK);
  if (rowA === rowB) return dedupe([a, b]);

  let bestX = 0;
  let bestCost = Infinity;
  let bestTie = Infinity;
  for (let i = -GRID; i <= GRID; i++) {
    const x = i * BLOCK;
    const cost = Math.abs(a.x - x) + Math.abs(b.x - x);
    const tie = Math.abs((prefer === 'a' ? a.x : b.x) - x);
    if (cost < bestCost - 0.01 || (Math.abs(cost - bestCost) <= 0.01 && tie < bestTie)) {
      bestX = x;
      bestCost = cost;
      bestTie = tie;
    }
  }
  return dedupe([a, { x: bestX, z: a.z }, { x: bestX, z: b.z }, b]);
}
