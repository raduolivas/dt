import { expect, test } from '@playwright/test';
import {
  createDelivery,
  ownClientIp,
  rotateLink,
  simulate,
  waitTrackable,
  type Model,
  type Status,
} from '../lib/stack';
import { HEAD, SUB, courierVisible, record, samples, statusOfHead, streamedStatuses } from '../lib/view';

// The hosted tracking page (GET /t/{token}) against the local stack, deliveries driven by the sandbox simulator.

const STORE = 'E2E Kitchen';
const ORDER: Status[] = ['confirmed', 'courier_assigned', 'at_pickup', 'picked_up', 'arriving', 'delivered'];
/** "Arrives in 12 min", "Arrives between 12:40–12:50", either with "estimated", or "Calculating delivery time…". */
const ETA_COPY =
  /^(Arrives in \d+ min|Arrives between \d{2}:\d{2}–\d{2}:\d{2})( estimated)?$|^Calculating delivery time…$/;

test.beforeEach(async ({ context }) => {
  await ownClientIp(context);
});

for (const model of ['direct', 'end_to_end'] as const satisfies Model[]) {
  test(`${model}: from the preparing view through every state to the final card`, async ({ page }) => {
    const d = await createDelivery(model);
    await waitTrackable(d.token);
    const streamed = streamedStatuses(page);
    await page.goto(d.url);

    // preparing: the store prepares the order, no courier anywhere
    const head = page.locator('[data-card="eta"] .dt-head');
    await expect(head).toHaveText(HEAD.confirmed);
    await expect(page.locator('[data-card="eta"] .dt-sub')).toHaveText(SUB(STORE).confirmed);
    await expect(page.locator('[data-card="courier"]')).toHaveCount(0);
    await expect(page.locator('.dt-attribution')).toHaveCount(1);

    await record(page);
    await simulate(d.id);
    await expect(page.locator('.dt-card-final .dt-head')).toHaveText('Delivered', { timeout: 300_000 });
    await expect(page.locator('.dt-card-final .dt-sub')).toHaveText(/^Delivered at \d{2}:\d{2}\. Enjoy!$/);
    await expect(page.locator('[data-card="eta"]')).toHaveCount(0);
    await expect(page.locator('[data-card="courier"]')).toHaveCount(0);

    const seen = await samples(page);
    const shown: Status[] = [];
    for (const s of seen) {
      if (!s.head) continue;
      const st = statusOfHead(s.head);
      expect(st, `unknown heading "${s.head}"`).toBeDefined();
      // the ETA card's copy for the state
      expect(s.sub).toBe(SUB(STORE)[st as Exclude<Status, 'delivered'>]);
      expect(s.eta).toMatch(ETA_COPY);
      // the courier shows only where the server lets the customer see it
      if (!courierVisible(model, st!)) {
        expect(s.courierCard, `courier card in ${st}`).toBe(false);
        expect(s.courierLabel, `courier label in ${st}`).toBe(false);
      }
      if (shown.at(-1) !== st) shown.push(st!);
    }
    // every state the server sent was shown, in order, and the states only move forward
    const sent = [...new Set(streamed)].filter(
      (s): s is Status => ORDER.includes(s as Status) && s !== 'delivered',
    );
    for (const s of sent) expect(shown, `state ${s} was sent but not shown`).toContain(s);
    expect(shown).toEqual([...shown].sort((a, b) => ORDER.indexOf(a) - ORDER.indexOf(b)));
    expect(shown[0]).toBe('confirmed');
    expect(shown).toContain('picked_up');
    // the courier appeared once visible
    expect(seen.some((s) => s.courierCard)).toBe(true);
  });
}

test('switches between 3D and 2D', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  await page.goto(d.url);
  await expect(page.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);

  const toggle = page.locator('.dt-mode');
  await expect(toggle).toHaveText('2D');
  await expect(toggle).toHaveAccessibleName('Show the map in 2D');
  await toggle.click();
  await expect(toggle).toHaveText('3D');
  await expect(toggle).toHaveAccessibleName('Show the map in 3D');
  await toggle.click();
  await expect(toggle).toHaveText('2D');
  await expect(page.locator('canvas.scene-canvas')).toHaveCount(1);
});

test('follows the color scheme: day and night', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  await page.emulateMedia({ colorScheme: 'light' });
  await page.goto(d.url);
  const root = page.locator('.dt-root');
  await expect(page.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);
  await expect(root).toHaveAttribute('data-theme', 'light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(root).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(root).toHaveAttribute('data-theme', 'light');
});

test('shows the inactive card after the link is rotated', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  await page.goto(d.url);
  await expect(page.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);

  await rotateLink(d.id);
  // the open stream is closed by the revocation; the page shows a calm card, never an error
  const inactive = page.locator('[data-card="inactive"] .dt-head');
  await expect(inactive).toHaveText('This tracking link is no longer active');
  await expect(page.locator('[data-card="eta"]')).toHaveCount(0);

  // and opening the old link again shows the same card
  await page.reload();
  await expect(inactive).toHaveText('This tracking link is no longer active');
});

test('shows "Getting your delivery ready…" when opened right after creation', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  // Right after creation the delivery may not have reached tracking yet, and the session answers
  // 503 tracking_unavailable with Retry-After: 1. That window is often shorter than a page load, so the first session
  // call gets the gateway's 503 here; the calls after it go to the stack.
  let first = true;
  await page.route('http://localhost:8080/v1/tracking/session', async (route) => {
    if (route.request().method() !== 'POST' || !first) return route.fallback();
    first = false;
    await route.fulfill({
      status: 503,
      headers: {
        'Content-Type': 'application/problem+json',
        'Retry-After': '1',
        'Access-Control-Allow-Origin': '*',
        'Access-Control-Expose-Headers': 'Retry-After, Request-Id',
      },
      body: JSON.stringify({
        type: 'about:blank',
        title: 'Service Unavailable',
        status: 503,
        code: 'tracking_unavailable',
      }),
    });
  });
  await page.goto(d.url);
  await expect(page.locator('[data-card="warming"] .dt-head')).toHaveText('Getting your delivery ready…');
  await expect(page.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);
  expect(first).toBe(false);
});
