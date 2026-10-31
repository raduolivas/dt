import * as THREE from 'three';
import { frameCamera } from '@dt/renderer/framing';
import { describe, expect, it } from 'vitest';
import { computeLayout, LINE_HEIGHT, type Layout } from './layout';

const PIN_LIFT = 34; // the renderer frames the pins above the points too

interface Route {
  points: THREE.Vector3[];
  /** The scene's pin labels: bottom centre over the model, size in CSS px (as laid out on a phone). */
  labels: { at: THREE.Vector3; width: number; height: number }[];
}

/** Store, courier half way, home (scene metres, y up), with a long store name and "Your address" over them. */
function route(store: THREE.Vector3, courier: THREE.Vector3, home: THREE.Vector3): Route {
  return {
    points: [store, courier, home],
    labels: [
      { at: store.clone().setY(36), width: 196, height: 24 }, // "Bakkerij van der Linden"
      { at: home.clone().setY(34), width: 106, height: 24 },
    ],
  };
}

// About 650 m, one running up the screen in the isometric view, one across it (where label widths matter most).
const ROUTES: Record<string, Route> = {
  'up the screen': route(new THREE.Vector3(-320, 0, -120), new THREE.Vector3(10, 0, 40), new THREE.Vector3(300, 0, 210)),
  'across the screen': route(new THREE.Vector3(-240, 0, 230), new THREE.Vector3(0, 0, 0), new THREE.Vector3(230, 0, -240)),
};

/** Where the renderer's framing puts the points, their pins and the label box corners on screen (CSS px). */
function onScreen(width: number, height: number, layout: Layout, r: Route) {
  const cam = new THREE.OrthographicCamera();
  frameCamera(cam, r.points, width, height, undefined, layout.insets, r.labels);
  cam.updateMatrixWorld(); // as rendering does
  const px = (p: THREE.Vector3) => {
    const v = p.clone().project(cam);
    return { x: ((v.x + 1) / 2) * width, y: ((1 - v.y) / 2) * height };
  };
  const points = r.points.flatMap((p) => [0, PIN_LIFT].map((dy) => px(p.clone().setY(p.y + dy))));
  // each label's box hangs above its anchor, centred
  const labels = r.labels.flatMap((l) => {
    const a = px(l.at);
    return [
      { x: a.x - l.width / 2, y: a.y - l.height },
      { x: a.x + l.width / 2, y: a.y },
    ];
  });
  return [...points, ...labels];
}

function expectInMap(width: number, height: number, layout: Layout) {
  const { left, top, width: w, height: h } = layout.map;
  expect(w).toBeGreaterThan(0);
  expect(h).toBeGreaterThan(0);
  for (const [name, r] of Object.entries(ROUTES)) {
    for (const p of onScreen(width, height, layout, r)) {
      expect(p.x, `${name} x`).toBeGreaterThanOrEqual(left);
      expect(p.x, `${name} x`).toBeLessThanOrEqual(left + w);
      expect(p.y, `${name} y`).toBeGreaterThanOrEqual(top);
      expect(p.y, `${name} y`).toBeLessThanOrEqual(top + h);
    }
  }
}

describe('computeLayout', () => {
  it('keeps the scene and its labels visible in iPhone SE landscape', () => {
    const l = computeLayout({ width: 667, height: 375, expanded: false });
    expect(l.kind).toBe('line');
    // the line is covered, and it is only a line
    expect(l.insets.bottom).toBeGreaterThanOrEqual(LINE_HEIGHT);
    expect(l.insets.bottom).toBeLessThanOrEqual(72);
    expectInMap(667, 375, l);
    // opened, the cards move to the side and the map stays beside them
    const open = computeLayout({ width: 667, height: 375, expanded: true });
    expect(open.kind).toBe('side');
    expect(open.insets.bottom).toBe(0);
    expect(open.insets.left).toBeGreaterThanOrEqual(open.sideWidth);
    expectInMap(667, 375, open);
  });

  it('keeps the scene and its labels visible in iPhone SE portrait with the sheet collapsed', () => {
    // the peek sheet as laid out: its top 235 px above the bottom edge
    const l = computeLayout({ width: 375, height: 667, expanded: false, panel: { left: 12, top: 667 - 235, width: 351, height: 223 } });
    expect(l.kind).toBe('sheet');
    expect(l.insets.bottom).toBe(235);
    expectInMap(375, 667, l);
  });

  it('uses a bottom sheet in iPhone SE and iPhone 14 portrait, the map above it', () => {
    for (const [w, h] of [[375, 667], [390, 844]] as const) {
      // measured: the sheet's top edge 222 px above the bottom
      const peek = computeLayout({ width: w, height: h, expanded: false, panel: { left: 12, top: h - 222, width: w - 24, height: 210 } });
      expect(peek.kind).toBe('sheet');
      expect(peek.insets.bottom).toBe(222);
      expectInMap(w, h, peek);
      // a tall sheet is capped so the map always keeps a strip
      const open = computeLayout({ width: w, height: h, expanded: true, panel: { left: 12, top: h - 2000, width: w - 24, height: 1988 } });
      expect(open.map.height).toBeGreaterThanOrEqual(h * 0.28 - 1);
      expectInMap(w, h, open);
    }
  });

  it('uses a side panel from 900 px wide', () => {
    const l = computeLayout({ width: 1280, height: 800, expanded: false });
    expect(l.kind).toBe('side');
    expect(l.insets.left).toBeGreaterThan(300);
    expect(l.insets.bottom).toBe(0);
    expectInMap(1280, 800, l);
    expect(computeLayout({ width: 899, height: 800, expanded: false }).kind).toBe('sheet');
  });
});
