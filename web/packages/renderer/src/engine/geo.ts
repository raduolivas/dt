import type { LatLng } from './types';

const EARTH_RADIUS_M = 6378137;
const DEG = Math.PI / 180;

/** Local planar coordinates in meters. x = east, z = SOUTH (so north is "up" on screen). */
export interface Vec2 {
  x: number;
  z: number;
}

/**
 * Equirectangular projection around a fixed origin. Accurate to well under a
 * meter at city scale, which is all the 3D scene needs.
 */
export class GeoProjection {
  private readonly cosLat: number;

  constructor(public readonly origin: LatLng) {
    this.cosLat = Math.cos(origin.lat * DEG);
  }

  toLatLng(p: Vec2): LatLng {
    return {
      lat: this.origin.lat - p.z / EARTH_RADIUS_M / DEG,
      lng: this.origin.lng + p.x / (EARTH_RADIUS_M * this.cosLat) / DEG,
    };
  }

  toLocal(ll: LatLng): Vec2 {
    return {
      x: (ll.lng - this.origin.lng) * DEG * EARTH_RADIUS_M * this.cosLat,
      z: -(ll.lat - this.origin.lat) * DEG * EARTH_RADIUS_M,
    };
  }
}

export function haversineM(a: LatLng, b: LatLng): number {
  const dLat = (b.lat - a.lat) * DEG;
  const dLng = (b.lng - a.lng) * DEG;
  const s =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(a.lat * DEG) * Math.cos(b.lat * DEG) * Math.sin(dLng / 2) ** 2;
  return 2 * EARTH_RADIUS_M * Math.asin(Math.sqrt(s));
}
