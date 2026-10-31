import { expect, test } from '@playwright/test';
import { DEMO_HTTP, createDelivery, ownClientIp, simulate, waitTrackable } from '../lib/stack';
import { HEAD } from '../lib/view';

// The SDK as a partner uses it: the demo page loads the bundle from the local CDN and mounts <dt-tracking>.

test.beforeEach(async ({ context }) => {
  await ownClientIp(context);
});

test('the demo page mounts <dt-tracking>, gets statuschange and delivered, and destroy() removes the canvas', async ({
  page,
}) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  await page.goto(`${DEMO_HTTP}/?token=${d.token}`);

  const view = page.locator('dt-tracking');
  await expect(view.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);
  await expect(view.locator('canvas.scene-canvas')).toHaveCount(1);
  await expect(view.locator('.dt-attribution')).toHaveCount(1);

  const events = () =>
    page.evaluate(
      () => (window as unknown as { dtDemo: { events: { type: string; detail: unknown }[] } }).dtDemo.events,
    );
  await expect.poll(async () => (await events()).map((e) => e.type)).toContain('ready');

  await simulate(d.id);
  await expect
    .poll(async () => (await events()).map((e) => e.type), { timeout: 300_000 })
    .toContain('delivered');
  const changes = (await events())
    .filter((e) => e.type === 'statuschange')
    .map((e) => (e.detail as { status: string }).status);
  expect(changes[0]).toBe('confirmed');
  expect(changes).toContain('picked_up');
  expect(changes.at(-1)).toBe('delivered');
  expect((await events()).filter((e) => e.type === 'delivered')).toHaveLength(1);
  await expect(view.locator('.dt-card-final .dt-head')).toHaveText('Delivered');

  await page.locator('#destroy').click();
  await expect(view.locator('canvas')).toHaveCount(0);
  await expect(view.locator('.dt-root')).toHaveCount(0);
});
