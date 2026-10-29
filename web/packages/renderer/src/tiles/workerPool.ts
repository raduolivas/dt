import type { BuildLayers, BuildRequest, WorkerIn, WorkerOut } from './build/decode';
// Inline: the worker source is bundled into the library as a blob, because the page and the SDK load their scripts
// from the CDN origin and `new Worker(crossOriginUrl)` is not allowed. CSP needs `worker-src blob:`.
import BuildWorker from './worker?worker&inline';

/** Builds tile geometry. The default runs `decodeTile` in Web Workers; tests inject one that calls it directly. */
export interface TileBuilder {
  build(req: BuildRequest, signal?: AbortSignal): Promise<BuildLayers>;
  dispose?(): void;
}

/** A small pool of geometry workers (tiles are independent, so two or three cores halve the first load). */
export function createWorkerBuilder(size = defaultSize()): TileBuilder {
  const workers: Worker[] = [];
  const busy = new Map<Worker, number>();
  const pending = new Map<number, { resolve: (l: BuildLayers) => void; reject: (e: Error) => void; w: Worker }>();
  let nextId = 1;

  const spawn = (): Worker => {
    const w = new BuildWorker();
    w.onmessage = (e: MessageEvent<WorkerOut>) => {
      const m = e.data;
      const p = pending.get(m.id);
      if (!p) return;
      pending.delete(m.id);
      busy.set(w, (busy.get(w) ?? 1) - 1);
      if (m.type === 'built') p.resolve(m.layers);
      else p.reject(new Error(m.message));
    };
    w.onerror = (e) => {
      for (const [id, p] of pending) {
        if (p.w !== w) continue;
        pending.delete(id);
        p.reject(new Error(e.message || 'tile worker failed'));
      }
      busy.set(w, 0);
    };
    workers.push(w);
    busy.set(w, 0);
    return w;
  };

  const pick = (): Worker => {
    let best: Worker | undefined;
    for (const w of workers) if (!best || busy.get(w)! < busy.get(best)!) best = w;
    if ((!best || busy.get(best)! > 0) && workers.length < size) return spawn();
    return best!;
  };

  return {
    build(req, signal) {
      return new Promise<BuildLayers>((resolve, reject) => {
        if (signal?.aborted) return reject(new DOMException('aborted', 'AbortError'));
        const w = pick();
        const id = nextId++;
        pending.set(id, { resolve, reject, w });
        busy.set(w, busy.get(w)! + 1);
        const msg: WorkerIn = { type: 'build', id, tile: req.tile, data: req.data, origin: req.origin, theme: req.theme };
        w.postMessage(msg); // the raw tile is copied, the caller keeps it for re-theming
      });
    },
    dispose() {
      for (const w of workers) w.terminate();
      workers.length = 0;
      for (const p of pending.values()) p.reject(new DOMException('disposed', 'AbortError'));
      pending.clear();
    },
  };
}

function defaultSize(): number {
  const n = typeof navigator !== 'undefined' ? navigator.hardwareConcurrency || 2 : 2;
  return Math.max(1, Math.min(3, n - 1));
}
