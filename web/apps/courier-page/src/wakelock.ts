// Keeps the screen on while the courier delivers (Screen Wake Lock API). The lock is dropped by the browser whenever
// the page is hidden, so it is taken again when the page becomes visible. Where the API is missing, `supported` is
// false and the page shows a "keep this screen on" notice instead.

interface Sentinel {
  release(): Promise<void>;
  addEventListener(type: 'release', fn: () => void): void;
}
interface WakeLockApi {
  request(type: 'screen'): Promise<Sentinel>;
}

export interface WakeLock {
  readonly supported: boolean;
  /** Take the lock now and keep it until `release()`. */
  hold(): void;
  release(): void;
}

export function createWakeLock(
  nav: { wakeLock?: WakeLockApi } = navigator as unknown as { wakeLock?: WakeLockApi },
  doc: Document = document,
): WakeLock {
  const api = nav.wakeLock;
  let wanted = false;
  let sentinel: Sentinel | undefined;
  let requesting = false;

  const acquire = async () => {
    if (!api || !wanted || sentinel || requesting || doc.visibilityState === 'hidden') return;
    requesting = true;
    try {
      const s = await api.request('screen');
      if (!wanted) {
        void s.release();
        return;
      }
      sentinel = s;
      s.addEventListener('release', () => {
        if (sentinel === s) sentinel = undefined;
      });
    } catch {
      /* refused (low battery, not allowed): the notice and the courier's own settings still apply */
    } finally {
      requesting = false;
    }
  };
  doc.addEventListener('visibilitychange', () => void acquire());

  return {
    supported: !!api,
    hold() {
      wanted = true;
      void acquire();
    },
    release() {
      wanted = false;
      const s = sentinel;
      sentinel = undefined;
      if (s) void s.release().catch(() => undefined);
    },
  };
}
