import type { Frame, Page } from '@playwright/test';
import type { Model, Status } from './stack';

// The tracking view lives in an open Shadow DOM (the hosted page's #root host, or <dt-tracking>); Playwright's CSS
// locators pierce it, so `.dt-root` and the card selectors work from the page.

/** The exact English customer copy per state (web/packages/tracking/src/i18n.ts). */
export const HEAD: Record<Exclude<Status, 'delivered'>, string> = {
  confirmed: 'Order confirmed',
  courier_assigned: 'Courier assigned',
  at_pickup: 'Courier at the store',
  picked_up: 'On the way',
  arriving: 'Almost there',
};
export const SUB = (store: string): Record<Exclude<Status, 'delivered'>, string> => ({
  confirmed: `${store} is preparing your order`,
  courier_assigned: `${store} is preparing your order`,
  at_pickup: 'Your order is almost ready to go',
  picked_up: 'Your order is on its way to you',
  arriving: 'Your courier is almost at your door',
});
export const statusOfHead = (head: string): Status | undefined =>
  (Object.entries(HEAD) as [Status, string][]).find(([, h]) => h === head)?.[0];

/** MVP §4.5: whether the customer may see the courier (mirrors the tracking service's table). */
export function courierVisible(model: Model, s: Status): boolean {
  if (s === 'picked_up' || s === 'arriving') return true;
  if (s === 'courier_assigned' || s === 'at_pickup') return model === 'end_to_end';
  return false;
}

/** What the view showed after one change. */
export interface Sample {
  cards: string[];
  head: string | null;
  sub: string | null;
  eta: string | null;
  final: string | null;
  finalSub: string | null;
  courierCard: boolean;
  /** The 3D scene's courier label is on screen (the 2D fallback has none). */
  courierLabel: boolean;
}

declare global {
  interface Window {
    dtSamples?: Sample[];
  }
}

/**
 * Records every change of the view (a MutationObserver on its shadow root), so a state that lasts a fraction of a
 * second at 20x simulation speed is still seen. Read the record with `samples`.
 */
export async function record(target: Page | Frame): Promise<void> {
  await target.evaluate(() => {
    const host = [...document.querySelectorAll('*')].find((e) => e.shadowRoot?.querySelector('.dt-root'));
    const root = host?.shadowRoot;
    if (!root) throw new Error('no tracking view on the page');
    const text = (e: Element | null | undefined) => e?.textContent?.trim() ?? null;
    const read = () => {
      const eta = root.querySelector('[data-card="eta"]');
      const fin = root.querySelector('.dt-card-final');
      const label = root.querySelector<HTMLElement>('.scene-label.courier');
      return {
        cards: [...root.querySelectorAll<HTMLElement>('[data-card]')].map((e) => e.dataset.card ?? ''),
        head: text(eta?.querySelector('.dt-head')),
        sub: text(eta?.querySelector('.dt-sub')),
        eta: text(eta?.querySelector('.dt-eta')),
        final: text(fin?.querySelector('.dt-head')),
        finalSub: text(fin?.querySelector('.dt-sub')),
        courierCard: root.querySelector('[data-card="courier"]') !== null,
        courierLabel: !!label && label.style.display !== 'none',
      };
    };
    const samples: ReturnType<typeof read>[] = [];
    let last = '';
    const take = () => {
      const s = read();
      const k = JSON.stringify(s);
      if (k !== last) {
        last = k;
        samples.push(s);
      }
    };
    window.dtSamples = samples;
    take();
    new MutationObserver(take).observe(root, { subtree: true, childList: true, characterData: true });
  });
}

export const samples = (target: Page | Frame): Promise<Sample[]> =>
  target.evaluate(() => window.dtSamples ?? []);

/** Statuses carried by the snapshots the page received over its WebSocket, in order (a delivery's own stream). */
export function streamedStatuses(page: Page): string[] {
  const seen: string[] = [];
  page.on('websocket', (ws) => {
    ws.on('framereceived', ({ payload }) => {
      try {
        const m = JSON.parse(String(payload)) as { type?: string; data?: { status?: string } };
        if (m.type === 'snapshot' && m.data?.status) seen.push(m.data.status);
      } catch {
        /* not JSON: ignore */
      }
    });
  });
  return seen;
}
