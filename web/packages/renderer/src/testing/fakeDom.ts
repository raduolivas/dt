// Test-only: just enough DOM for the renderer to run headless in Vitest (no jsdom in this package).
import type * as THREE from 'three';
import { vi } from 'vitest';

export interface DrawOp {
  op: string;
  args: unknown[];
  fillStyle: unknown;
  strokeStyle: unknown;
  globalAlpha: unknown;
}

/** A 2D context that records every call with the styles in effect. */
export type Fake2d = CanvasRenderingContext2D & { log: DrawOp[] };

export function fake2d(): Fake2d {
  const log: DrawOp[] = [];
  const state: Record<string | symbol, unknown> = { fillStyle: '#000000', strokeStyle: '#000000', globalAlpha: 1, lineWidth: 1 };
  return new Proxy(state, {
    get(t, k) {
      if (k === 'log') return log;
      if (k in t) return t[k];
      if (typeof k !== 'string') return undefined;
      if (k === 'measureText') return (s: string) => ({ width: s.length * 7 });
      if (k.startsWith('create')) return () => ({ addColorStop() {} });
      return (...args: unknown[]) => {
        log.push({ op: k, args, fillStyle: t.fillStyle, strokeStyle: t.strokeStyle, globalAlpha: t.globalAlpha });
      };
    },
    set(t, k, v) {
      t[k] = v;
      return true;
    },
  }) as unknown as Fake2d;
}

export class FakeElement extends EventTarget {
  className = '';
  textContent = '';
  hidden = false;
  width = 300;
  height = 150;
  clientWidth = 390;
  clientHeight = 844;
  readonly style: Record<string, string> = {};
  readonly dataset: Record<string, string> = {};
  readonly children: FakeElement[] = [];
  parent: FakeElement | null = null;
  /** What `getContext(kind)` returns. */
  contexts: Record<string, unknown> = {};
  readonly classList = {
    toggle: (c: string, on?: boolean) => {
      const set = new Set(this.className.split(' ').filter(Boolean));
      if (on ?? !set.has(c)) set.add(c);
      else set.delete(c);
      this.className = [...set].join(' ');
    },
    contains: (c: string) => this.className.split(' ').includes(c),
  };

  constructor(
    readonly tagName: string,
    readonly ownerDocument: EventTarget,
  ) {
    super();
  }

  appendChild<T extends FakeElement>(c: T): T {
    c.remove();
    c.parent = this;
    this.children.push(c);
    return c;
  }

  remove(): void {
    if (!this.parent) return;
    const i = this.parent.children.indexOf(this);
    if (i >= 0) this.parent.children.splice(i, 1);
    this.parent = null;
  }

  getRootNode(): EventTarget {
    return this.ownerDocument;
  }

  getContext(kind: string): unknown {
    return this.contexts[kind] ?? null;
  }

  /** Every descendant (depth first). */
  all(): FakeElement[] {
    return this.children.flatMap((c) => [c, ...c.all()]);
  }

  /** The text of this element and its descendants. */
  get innerText(): string {
    return [this.textContent, ...this.children.map((c) => c.innerText)].join('');
  }
}

export interface FakeDom {
  container: FakeElement;
  /** Contexts a new `<canvas>` hands out (`webgl2`, `webgl`, `2d`). */
  canvasContexts: Record<string, () => unknown>;
  canvases: FakeElement[];
  /** Runs the queued animation frames once. */
  frame(): void;
}

/** Stubs `document`, `window`, `ResizeObserver` and `requestAnimationFrame`; undo with `vi.unstubAllGlobals()`. */
export function installFakeDom(): FakeDom {
  const doc = new EventTarget() as EventTarget & { createElement(tag: string): FakeElement };
  const dom: FakeDom = {
    container: new FakeElement('div', doc),
    canvasContexts: {},
    canvases: [],
    frame() {
      const run = [...queue.values()];
      queue.clear();
      run.forEach((f) => f(performance.now()));
    },
  };
  doc.createElement = (tag: string) => {
    const el = new FakeElement(tag, doc);
    if (tag === 'canvas') {
      for (const [k, make] of Object.entries(dom.canvasContexts)) el.contexts[k] = make();
      dom.canvases.push(el);
    }
    return el;
  };
  const queue = new Map<number, FrameRequestCallback>();
  let next = 1;
  vi.stubGlobal('document', doc);
  vi.stubGlobal('window', { devicePixelRatio: 2 });
  vi.stubGlobal('devicePixelRatio', 2);
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal('requestAnimationFrame', (f: FrameRequestCallback) => {
    queue.set(next, f);
    return next++;
  });
  vi.stubGlobal('cancelAnimationFrame', (id: number) => queue.delete(id));
  return dom;
}

/** A stand-in for `THREE.WebGLRenderer`: counts renders, keeps the settings the scene gives it. */
export function mockWebGL(doc: { createElement(tag: string): unknown } = document as never) {
  const canvas = doc.createElement('canvas') as FakeElement;
  const r = {
    domElement: canvas,
    renders: 0,
    pixelRatio: 1,
    shadowMap: { enabled: false, type: 0, autoUpdate: true, needsUpdate: false },
    outputColorSpace: '',
    autoClear: true,
    info: { autoReset: true, reset() {}, render: { calls: 0 } },
    setPixelRatio(p: number) {
      r.pixelRatio = p;
    },
    getPixelRatio: () => r.pixelRatio,
    setSize() {},
    /** The last scene drawn. */
    lastScene: null as THREE.Scene | null,
    render(scene: THREE.Scene) {
      r.renders++;
      r.lastScene = scene;
    },
    /** `forceContextLoss` and `dispose` calls, in order. */
    calls: [] as string[],
    /** Like the browser: the canvas fires `webglcontextlost` (at once here, which is stricter). */
    forceContextLoss() {
      r.calls.push('forceContextLoss');
      canvas.dispatchEvent(new Event('webglcontextlost', { cancelable: true }));
    },
    dispose() {
      r.calls.push('dispose');
    },
  };
  return r as unknown as typeof r & THREE.WebGLRenderer;
}
