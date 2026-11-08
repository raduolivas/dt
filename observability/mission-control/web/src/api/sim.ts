/** Simulation mode client. /api/sim/* exists only when mc-backend runs with MC_SIMULATION=1; elsewhere it is a 404. */
export interface SimRun {
  delivery_id: string;
  order_ref: string;
  scenario: number;
  picked_up: boolean;
}

export interface SimClient {
  /** The live simulated runs, or null when simulation mode is off (or unreachable). */
  status(): Promise<SimRun[] | null>;
  breakRun(deliveryId: string, scenario: number): Promise<void>;
  heal(deliveryId: string): Promise<void>;
}

export function createSimClient(f: typeof fetch = (...a) => fetch(...a)): SimClient {
  const post = async (path: string, body?: unknown) => {
    const res = await f(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try {
        const b = (await res.json()) as { error?: string };
        if (typeof b.error === 'string') msg = b.error;
      } catch {
        // keep the status
      }
      throw new Error(msg);
    }
  };
  return {
    async status() {
      try {
        const res = await f('/api/sim/status', { headers: { Accept: 'application/json' } });
        if (!res.ok) return null;
        const b = (await res.json()) as { enabled?: boolean; runs?: SimRun[] };
        return b.enabled ? (b.runs ?? []) : null;
      } catch {
        return null;
      }
    },
    breakRun: (id, scenario) => post(`/api/sim/runs/${encodeURIComponent(id)}/break`, { scenario }),
    heal: (id) => post(`/api/sim/runs/${encodeURIComponent(id)}/heal`),
  };
}

export const simApi: SimClient = createSimClient();
