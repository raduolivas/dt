# Delivery Tracker — 3D tracking module (prototype)

A stylized isometric 3D simulation of a delivery: a courier on a bicycle rides to a store, waits for the
order, picks it up and delivers it to the customer's home. Built with **Three.js + Vite + TypeScript**.

It is designed as a **module**, not an app: the tracking engine and the 3D view are separated by one small
data contract, so you can later replace the simulator with real GPS data from your backend.

```bash
npm install
npm run dev      # http://localhost:5173
npm run build    # type-check + production build in dist/
```

## Architecture

```
src/
  engine/              <- "backend": no Three.js, no DOM
    types.ts           TrackingSnapshot, TrackingSource, SimParams  (THE CONTRACT)
    geo.ts             lat/lng <-> local meters projection
    path.ts            walk along a polyline (position + bearing by distance)
    router.ts          Manhattan router for the prototype city
    world.ts           city layout, store / home places, geo ORIGIN
    simulator.ts       order state machine + fake GPS (implements TrackingSource)
  view/                <- 3D, consumes snapshots only
    CityScene.ts       scene, courier, route lines, pins, camera follow
    models.ts          bicycle + rider, store, house, buildings, trees
    ribbon.ts          road-following route ribbon
  ui.ts, style.css     HUD (stats, timeline, stepper, parameters)
  main.ts              wires simulator -> scene + HUD
```

## The contract

The view and the HUD only ever receive this object. Everything is real geographic data (lat/lng, compass
heading in degrees, meters, seconds):

```ts
interface TrackingSnapshot {
  orderId: string;
  status: 'requested' | 'accepted' | 'to_store' | 'at_store'
        | 'picked_up' | 'on_the_way' | 'arriving' | 'delivered';
  courier: { position: LatLng; heading: number /* 0 = north, clockwise */; speedKmh: number };
  store: LatLng;
  destination: LatLng;
  route: { toStore: LatLng[]; toCustomer: LatLng[] };   // polylines
  etaSeconds: number | null;
  distanceRemainingM: number;
  progress: number;                                      // 0..1
  elapsedSec: number;
  updatedAt: number;                                     // unix ms
}

interface TrackingSource {
  subscribe(listener: (s: TrackingSnapshot) => void): () => void;
}
```

Open **Parameters → Live snapshot** in the running prototype to watch this JSON update in real time.

## Plugging into a real app

1. **Backend side**: emit `TrackingSnapshot`s (WebSocket / SSE / polling / Firebase). The courier app sends GPS
   fixes, your backend adds status, route (e.g. from OSRM, Mapbox or Google Directions) and ETA.
2. **Client side**: write a small `TrackingSource`:

   ```ts
   class WebSocketSource implements TrackingSource {
     private listeners = new Set<SnapshotListener>();
     constructor(url: string) {
       const ws = new WebSocket(url);
       ws.onmessage = (e) => this.listeners.forEach((l) => l(JSON.parse(e.data)));
     }
     subscribe(l: SnapshotListener) { this.listeners.add(l); return () => this.listeners.delete(l); }
   }

   const source = new WebSocketSource('wss://api.example.com/orders/123/track');
   source.subscribe((s) => { scene.onSnapshot(s); hud.update(s, true); });
   ```

3. `CityScene` already interpolates between snapshots (position and heading), so 1 GPS fix per second still
   looks fluid. Wrap `CityScene` in a React/Vue component, or load it in a WebView for a mobile app.

### What is still prototype-only

- **The city is stylized and fixed.** Real coordinates are projected around `ORIGIN` in `engine/world.ts`; the
  store and home sit on the street lattice. For real orders, the next step is to generate the scene around the
  order (store at the origin, route from the snapshot) or to draw real building footprints from map data.
- **`router.ts`** is a Manhattan router that only works on the lattice. A real backend provides the route.
- **Visual scale**: the bicycle is drawn ~6x larger than real so it reads from the isometric camera.
- Driving ETA is a simple speed-based estimate; a real ETA comes from your backend.

## Simulation parameters

| Parameter | Default | Effect |
|---|---|---|
| Courier speed | 18 km/h | Cruising speed (slows down at corners and on arrival) |
| Store preparation time | 25 s | How long the courier waits at the store |
| Simulation speed | 4x | Simulated seconds per real second |
| Delivery distance | Medium | Near ≈ 770 m, Medium ≈ 1.1 km, Far ≈ 1.4 km total route |

Controls: **Request order / Pause / Resume**, **Restart**, **Follow** (camera follows the courier),
**Overview** (whole city). Space bar toggles run/pause. Drag to rotate, scroll or pinch to zoom.
