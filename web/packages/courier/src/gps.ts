import type { CourierFix } from './types';

export const MAX_ACCURACY_M = 50;

export type GpsState = 'off' | 'asking' | 'denied' | 'on';

/** Wraps watchPosition. Nothing runs until `start()`; `stop()` clears the watch. */
export function createGps(
  geo: Geolocation | undefined,
  onFix: (f: CourierFix) => void,
  onState: (s: GpsState) => void,
) {
  let watch: number | null = null;
  let state: GpsState = 'off';
  const set = (s: GpsState) => {
    if (s === state) return;
    state = s;
    onState(s);
  };

  return {
    get state() {
      return state;
    },
    start() {
      if (watch !== null || !geo) return;
      set('asking');
      watch = geo.watchPosition(
        (p) => {
          set('on');
          const c = p.coords;
          // Poor fixes are dropped here and never buffered: a 200 m jump is worse than a gap.
          if (!(c.accuracy <= MAX_ACCURACY_M)) return;
          const fix: CourierFix = {
            t: new Date(p.timestamp).toISOString(), // the device's reading time, not the send time
            lat: c.latitude,
            lng: c.longitude,
            accuracy_m: c.accuracy,
          };
          if (c.speed != null && Number.isFinite(c.speed)) fix.speed_mps = c.speed;
          if (c.heading != null && Number.isFinite(c.heading)) fix.heading_deg = c.heading;
          onFix(fix);
        },
        (e) => {
          // PERMISSION_DENIED is final until the user changes it; other errors (no signal, timeout) are transient.
          if (e.code === 1) {
            if (watch !== null) geo.clearWatch(watch);
            watch = null;
            set('denied');
          }
        },
        { enableHighAccuracy: true, maximumAge: 0, timeout: 30_000 },
      );
    },
    stop() {
      if (watch !== null) geo?.clearWatch(watch);
      watch = null;
      set('off');
    },
  };
}
