import * as THREE from 'three';
import { frameCamera } from '@dt/renderer/framing';
import { describe, expect, it } from 'vitest';
import { computeLayout, LINE_HEIGHT, type Layout } from './layout';

// A 650 m route seen by the renderer's camera: store, courier half way, home (scene metres, y up).
const STORE = new THREE.Vector3(-320, 0, -120);
const COURIER = new THREE.Vector3(10, 0, 40);
const HOME = new THREE.Vector3(300, 0, 210);
const PIN_LIFT = 34; // the renderer frames the pins above the points too

/** Where the renderer's framing puts the points on screen (CSS px) with the layout's insets. */
function onScreen(width: number, height: number, layout: Layout) {
  const cam = new THREE.OrthographicCamera();
  frameCamera(cam, [STORE, COURIER, HOME], width, height, undefined, layout.insets);
  cam.updateMatrixWorld(); // as rendering does
  return [STORE, COURIER, HOME].flatMap((p) =>
    [0, PIN_LIFT].map((dy) => {
      const v = p.clone().setY(p.y + dy).project(cam);
      return { x: ((v.x + 1) / 2) * width, y: ((1 - v.y) / 2) * height };
    }),
  );
}

function expectInMap(width: number, height: number, layout: Layout) {
  const { left, top, width: w, height: h } = layout.map;
  expect(w).toBeGreaterThan(0);
  expect(h).toBeGreaterThan(0);
  for (const p of onScreen(width, height, layout)) {
    expect(p.x).toBeGreaterThanOrEqual(left);
    expect(p.x).toBeLessThanOrEqual(left + w);
    expect(p.y).toBeGreaterThanOrEqual(top);
    expect(p.y).toBeLessThanOrEqual(top + h);
  }
}

describe('computeLayout', () => {
  it('keeps the scene visible in iPhone SE landscape', () => {
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
