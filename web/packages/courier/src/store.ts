export type Kind = 'fixes' | 'taps';
export interface Entry<T> {
  key: number;
  value: T;
}

/** A durable copy of the offline buffer, so a killed tab loses neither taps nor fixes. IndexedDB, else memory. */
export interface BufferStore {
  load(): Promise<{ fixes: Entry<unknown>[]; taps: Entry<unknown>[] }>;
  /** Writes are queued in order and never throw; a failed write only costs durability. */
  put(kind: Kind, key: number, value: unknown): void;
  remove(kind: Kind, keys: number[]): void;
  clear(): void;
  /** Resolves when every queued write has finished (tests, and `end()`). */
  settled(): Promise<void>;
}

interface Backend {
  all(kind: Kind): Promise<Entry<unknown>[]>;
  put(kind: Kind, key: number, value: unknown): Promise<void>;
  remove(kind: Kind, keys: number[]): Promise<void>;
  clear(): Promise<void>;
}

function memoryBackend(): Backend {
  const m: Record<Kind, Map<number, unknown>> = { fixes: new Map(), taps: new Map() };
  return {
    async all(k) {
      return [...m[k]].map(([key, value]) => ({ key, value })).sort((a, b) => a.key - b.key);
    },
    async put(k, key, value) {
      m[k].set(key, value);
    },
    async remove(k, keys) {
      for (const key of keys) m[k].delete(key);
    },
    async clear() {
      m.fixes.clear();
      m.taps.clear();
    },
  };
}

const done = (tx: IDBTransaction) =>
  new Promise<void>((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = tx.onabort = () => reject(tx.error);
  });
const result = <T>(r: IDBRequest<T>) =>
  new Promise<T>((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });

function idbBackend(factory: IDBFactory): Promise<Backend> {
  return new Promise((resolve, reject) => {
    const req = factory.open('dt-courier', 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore('fixes');
      req.result.createObjectStore('taps');
    };
    req.onerror = req.onblocked = () => reject(req.error);
    req.onsuccess = () => {
      const db = req.result;
      resolve({
        async all(k) {
          const s = db.transaction(k).objectStore(k);
          const [keys, values] = await Promise.all([result(s.getAllKeys()), result(s.getAll())]);
          return keys.map((key, i) => ({ key: key as number, value: values[i] as unknown }));
        },
        async put(k, key, value) {
          const tx = db.transaction(k, 'readwrite');
          tx.objectStore(k).put(value, key);
          await done(tx);
        },
        async remove(k, keys) {
          const tx = db.transaction(k, 'readwrite');
          for (const key of keys) tx.objectStore(k).delete(key);
          await done(tx);
        },
        async clear() {
          const tx = db.transaction(['fixes', 'taps'], 'readwrite');
          tx.objectStore('fixes').clear();
          tx.objectStore('taps').clear();
          await done(tx);
        },
      });
    };
  });
}

export function createBufferStore(factory: IDBFactory | undefined): BufferStore {
  // IndexedDB can be missing or blocked (private windows, some webviews): then the buffer lives in memory only.
  const backend: Promise<Backend> = factory
    ? idbBackend(factory).catch(() => memoryBackend())
    : Promise.resolve(memoryBackend());
  let chain: Promise<unknown> = Promise.resolve();
  const enqueue = (op: (b: Backend) => Promise<unknown>) => {
    chain = chain.then(async () => op(await backend)).catch(() => undefined);
  };
  return {
    async load() {
      await chain;
      const b = await backend;
      try {
        const [fixes, taps] = await Promise.all([b.all('fixes'), b.all('taps')]);
        return { fixes, taps };
      } catch {
        return { fixes: [], taps: [] };
      }
    },
    put: (k, key, value) => enqueue((b) => b.put(k, key, value)),
    remove: (k, keys) => enqueue((b) => b.remove(k, keys)),
    clear: () => enqueue((b) => b.clear()),
    settled: async () => void (await chain),
  };
}
