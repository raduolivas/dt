/// <reference lib="webworker" />
import { decodeTile, transferList, type WorkerIn, type WorkerOut } from './build/decode';

// Bundled inline by the main thread (`import W from './tiles/worker?worker&inline'`) because the page loads its scripts
// from the CDN origin and a cross-origin `new Worker(url)` is not allowed. CSP: `worker-src blob:`.
const ctx = self as unknown as { postMessage(m: WorkerOut, t: Transferable[]): void; onmessage: ((e: MessageEvent<WorkerIn>) => void) | null };

ctx.onmessage = (e) => {
  const m = e.data;
  if (m.type !== 'build') return;
  try {
    const { layers } = decodeTile({ tile: m.tile, data: m.data, origin: m.origin, theme: m.theme });
    ctx.postMessage({ type: 'built', id: m.id, layers }, transferList(layers));
  } catch (err) {
    ctx.postMessage({ type: 'error', id: m.id, message: err instanceof Error ? err.message : String(err) }, []);
  }
};
