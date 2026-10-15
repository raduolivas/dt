import type { Vec2 } from './geo';

/** A polyline you can walk along by distance. */
export class Path {
  readonly length: number;
  private readonly cum: number[] = [0];

  constructor(readonly points: Vec2[]) {
    for (let i = 1; i < points.length; i++) {
      const a = points[i - 1];
      const b = points[i];
      this.cum.push(this.cum[i - 1] + Math.hypot(b.x - a.x, b.z - a.z));
    }
    this.length = this.cum[this.cum.length - 1] ?? 0;
  }

  /** Position and compass bearing (radians, 0 = north, clockwise) at distance `d`. */
  at(d: number): { pos: Vec2; bearing: number } {
    const pts = this.points;
    if (pts.length < 2) return { pos: pts[0] ?? { x: 0, z: 0 }, bearing: 0 };
    const dist = Math.min(Math.max(d, 0), this.length);
    let i = 1;
    while (i < pts.length - 1 && this.cum[i] < dist) i++;
    const a = pts[i - 1];
    const b = pts[i];
    const segLen = this.cum[i] - this.cum[i - 1] || 1;
    const t = (dist - this.cum[i - 1]) / segLen;
    return {
      pos: { x: a.x + (b.x - a.x) * t, z: a.z + (b.z - a.z) * t },
      bearing: bearingOf(a, b),
    };
  }

  /** Distance from `d` to the next interior corner (Infinity if none). */
  distanceToNextCorner(d: number): number {
    for (let i = 1; i < this.points.length - 1; i++) {
      if (this.cum[i] > d + 0.001) return this.cum[i] - d;
    }
    return Infinity;
  }
}

/** Compass bearing in radians for a move from a to b in local (x east, z south) coords. */
export function bearingOf(a: Vec2, b: Vec2): number {
  return Math.atan2(b.x - a.x, -(b.z - a.z));
}
