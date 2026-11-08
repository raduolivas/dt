// Pulse engine: turns observed events and server bundles into travelling
// pulses, outside React. The scene reads it once per animation frame (useFrame)
// and writes instance matrices; nothing re-renders per pulse. Pure TypeScript.
import type { BundleData, McEvent } from '../api/types';
import { RANK, toPulses, WINDOW_MS, type Pulse, type PulseColor } from '../state/pulses';
import type { McState } from '../state/store';
import type { SceneModel } from './model';
import { pulseColor } from './visualState';

/** Fixed visual speed in world units per second: pulses show that something happened, not its latency. */
export const SPEED = 9;
const MIN_DURATION_MS = 450;
/** A batch that arrives together keeps its order, spread over at most this long. */
export const MAX_STAGGER_MS = 1500;
/** Only events Mission Control observed this recently are animated (older ones are history). */
export const LIVE_WINDOW_MS = 30_000;
/** With reduced motion a static marker stays this long, carrying the count. */
export const MARKER_HOLD_MS = 3000;

interface Active {
  key: string;
  wire: string;
  forward: boolean;
  color: PulseColor;
  simulated: boolean;
  count: number;
  bundle: boolean;
  eventId?: number;
  start: number;
  duration: number;
}

export interface FrameItem {
  key: string;
  wire: string;
  /** Position along the wire from its a end (0) to its b end (1). */
  t: number;
  color: PulseColor;
  simulated: boolean;
  /** Observed events this item stands for. */
  count: number;
  bundle: boolean;
  /** Reduced motion: a marker that does not travel. */
  static: boolean;
  /** The event a click selects: the pulse's own, or the newest of a bundle. Absent for server bundles (they carry no ids). */
  eventId?: number;
}


/** Replay shows the pulses of this long before the cursor (R34). */
export const REPLAY_WINDOW_MS = 5000;

interface MarkerSrc {
  wire: string;
  color: PulseColor;
  simulated: boolean;
  count: number;
  eventId?: number;
}

/** One static marker per wire and colour, side by side (ok | unknown | warn | fail) so markers on one wire never overlap. */
function markerItems(list: readonly MarkerSrc[]): FrameItem[] {
  const markers = new Map<string, FrameItem>();
  for (const a of list) {
    const key = `m:${a.wire}:${a.color}`;
    const m = markers.get(key);
    if (m) {
      m.count += a.count;
      m.simulated ||= a.simulated;
      if (a.eventId !== undefined && (m.eventId === undefined || a.eventId > m.eventId)) m.eventId = a.eventId;
    } else {
      markers.set(key, { key, wire: a.wire, t: 0.32 + 0.12 * RANK[a.color], color: a.color, simulated: a.simulated, count: a.count, bundle: true, static: true, eventId: a.eventId });
    }
  }
  return [...markers.values()].sort((x, y) => RANK[x.color] - RANK[y.color]);
}

export class PulseEngine {
  private active: Active[] = [];
  private replayMemo: { events: readonly McEvent[]; cursor: number; items: FrameItem[] } | null = null;
  private seq = 0;
  // sync() cursors
  private started = false;
  private lastId = -Infinity;
  private lastEvents: readonly McEvent[] | null = null;
  private lastBundleSeq = -Infinity;
  private bundleHighWater = -Infinity;

  constructor(
    private readonly model: SceneModel,
    private readonly lengthOf: (wire: string) => number,
  ) {}

  private duration(wire: string): number {
    return Math.max(MIN_DURATION_MS, (this.lengthOf(wire) / SPEED) * 1000);
  }

  private push(a: Active) {
    this.active.push(a);
  }

  /**
   * Spawns pulses for freshly arrived events: one per event, or one bundle
   * pulse {count} for a wire with more than 20 events in one second (toPulses,
   * applied per occurred-at second so late batches are not filtered away).
   * Returns the number of pulses spawned.
   */
  /** Pulses for a batch of events: attribution gates (R30 / R33), one per event or a bundle per wire and second. */
  private pulsesFor(fresh: readonly McEvent[]): Pulse[] {
    const windows = new Map<number, McEvent[]>();
    for (const e of fresh) {
      // an event at one part is drawn only when its attribution passes the R30 / R33 gates
      if (e.source && e.source === e.target && !this.model.placeEvent(e)) continue;
      const at = Date.parse(e.occurredAt);
      if (!Number.isFinite(at)) continue;
      const end = Math.ceil(at / WINDOW_MS) * WINDOW_MS; // toPulses covers (end - WINDOW_MS, end]
      const l = windows.get(end);
      if (l) l.push(e);
      else windows.set(end, [e]);
    }
    return [...windows].flatMap(([end, evs]) => toPulses(evs, end)).sort((a, b) => a.at - b.at);
  }

  ingestEvents(fresh: readonly McEvent[], now: number): number {
    const pulses = this.pulsesFor(fresh);
    if (pulses.length === 0) return 0;
    const first = pulses[0].at;
    const spread = pulses[pulses.length - 1].at - first;
    const scale = spread > MAX_STAGGER_MS ? MAX_STAGGER_MS / spread : 1;
    let n = 0;
    for (const p of pulses) {
      const [source, target] = p.wire.split('>');
      const place = this.model.place(source, target ?? '', true);
      if (!place) continue;
      this.push({
        key: p.id, wire: place.wire, forward: place.forward, color: p.color, simulated: p.simulated,
        count: p.count ?? 1, bundle: p.count !== undefined, eventId: p.eventId, start: now + (p.at - first) * scale,
        duration: this.duration(place.wire),
      });
      n++;
    }
    return n;
  }

  /**
   * A server bundle (events the SSE stream folded under load): one pulse with
   * its count. Bundles carry no kind, so only cross-cube wires are drawn (a
   * self wire may be logs or state changes); returns whether it was drawn.
   */
  ingestBundle(b: BundleData, now: number): boolean {
    const [source, target] = b.wire.split('>');
    if (!target || b.count <= 0) return false;
    const place = this.model.place(source, target);
    if (!place) return false;
    this.push({
      key: `sb:${++this.seq}`, wire: place.wire, forward: place.forward, color: pulseColor(b.outcome), simulated: false,
      count: b.count, bundle: true, start: now, duration: this.duration(place.wire),
    });
    return true;
  }

  /**
   * Reads what is new in the store since the last call. History already in the
   * buffer at the first call, events observed more than LIVE_WINDOW_MS ago and
   * events at or below a server bundle's high-water mark (which may be counted
   * in that bundle) are not animated.
   */
  sync(s: McState, now: number): void {
    const evs = s.events;
    const newestBundle = s.bundles.length ? s.bundles[s.bundles.length - 1].seq : -Infinity;
    if (!this.started) {
      this.started = true;
      this.lastId = evs.length ? evs[evs.length - 1].id : -Infinity;
      this.lastEvents = evs;
      this.lastBundleSeq = newestBundle;
      for (const b of s.bundles) this.bundleHighWater = Math.max(this.bundleHighWater, b.streamId ?? -Infinity);
      return;
    }
    // Events first, against the high-water mark from before this sync: an event delivered
    // ahead of a bundle in the same batch is its own pulse, not part of that bundle.
    if (evs !== this.lastEvents) {
      this.lastEvents = evs;
      let i = evs.length;
      while (i > 0 && evs[i - 1].id > this.lastId) i--;
      if (i < evs.length) {
        const fresh: McEvent[] = [];
        for (let j = i; j < evs.length; j++) {
          const e = evs[j];
          const seen = Date.parse(e.observedAt);
          if (e.id <= this.bundleHighWater || !(now - seen <= LIVE_WINDOW_MS)) continue;
          fresh.push(e);
        }
        this.lastId = evs[evs.length - 1].id;
        if (fresh.length) this.ingestEvents(fresh, now);
      }
    }
    if (newestBundle > this.lastBundleSeq) {
      for (const b of s.bundles) {
        if (b.seq <= this.lastBundleSeq) continue;
        this.ingestBundle(b, now);
        if (b.streamId !== undefined) this.bundleHighWater = Math.max(this.bundleHighWater, b.streamId);
      }
      this.lastBundleSeq = newestBundle;
    }
  }

  /** Drops everything in flight (an order switch). The cursors keep moving forward. */
  clear(): void {
    this.active = [];
  }

  /**
   * Visits what to draw at `now`. Moving: one item per pulse in flight, t
   * oriented a -> b. Reduced motion: one static marker per wire and colour at
   * the wire's middle, carrying the count of pulses seen there recently.
   */
  each(now: number, reduced: boolean, fn: (i: FrameItem) => void): void {
    const keep = this.active.filter((a) => now < a.start + Math.max(a.duration, MARKER_HOLD_MS));
    this.active = keep;
    if (!reduced) {
      for (const a of keep) {
        if (now < a.start || now >= a.start + a.duration) continue;
        const u = (now - a.start) / a.duration;
        fn({
          key: a.key, wire: a.wire, t: a.forward ? u : 1 - u, color: a.color, simulated: a.simulated, count: a.count,
          bundle: a.bundle, static: false, eventId: a.eventId,
        });
      }
      return;
    }
    markerItems(keep.filter((a) => now >= a.start)).forEach(fn);
  }

  /**
   * Replay (R34): static counted markers, in the reduced-motion style, for the
   * pulse events in [cursor - REPLAY_WINDOW_MS, cursor]. Same gating and
   * bundling as live (pulsesFor, placement), so nothing is invented. Nothing
   * travels and nothing is sent anywhere.
   */
  replayMarkers(events: readonly McEvent[], cursor: number): FrameItem[] {
    const memo = this.replayMemo;
    if (memo && memo.events === events && memo.cursor === cursor) return memo.items;
    const items = this.computeMarkers(events, cursor);
    this.replayMemo = { events, cursor, items };
    return items;
  }

  private computeMarkers(events: readonly McEvent[], cursor: number): FrameItem[] {
    const lo = cursor - REPLAY_WINDOW_MS;
    const inWindow = events.filter((e) => {
      const at = Date.parse(e.occurredAt);
      return at >= lo && at <= cursor;
    });
    const placed: MarkerSrc[] = [];
    for (const p of this.pulsesFor(inWindow)) {
      const [source, target] = p.wire.split('>');
      const place = this.model.place(source, target ?? '', true);
      if (place) placed.push({ wire: place.wire, color: p.color, simulated: p.simulated, count: p.count ?? 1, eventId: p.eventId });
    }
    return markerItems(placed);
  }

  frame(now: number, reduced: boolean): FrameItem[] {
    const out: FrameItem[] = [];
    this.each(now, reduced, (i) => out.push(i));
    return out;
  }
}
