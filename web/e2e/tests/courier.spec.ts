import { expect, test } from '@playwright/test';
import { createRide, revokeSession, route } from '../lib/courier';
import { ownClientIp, status, waitTrackable } from '../lib/stack';
import { HEAD } from '../lib/view';

// The courier page (courier link, SDK, location batches, taps) driving the customer's tracking page, on the local
// stack. Direct deliveries: the customer sees the courier from `picked_up`.

test('courier page drives a delivery to delivered', async ({ browser }) => {
  const ride = await createRide();
  await waitTrackable(ride.delivery.token);

  const courierCtx = await browser.newContext({
    geolocation: { ...route(10)[0], accuracy: 8 },
    permissions: ['geolocation'],
    ...(test.info().project.use as object),
  });
  const trackCtx = await browser.newContext(test.info().project.use);
  await ownClientIp(trackCtx);
  const cp = await courierCtx.newPage();
  const tp = await trackCtx.newPage();
  try {
    await tp.goto(ride.delivery.url);
    await expect(tp.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);

    await cp.goto(ride.link);
    await cp.getByRole('button', { name: 'Start shift' }).click();
    await expect(cp.locator('article.delivery')).toHaveCount(1);
    await expect(cp.locator('[data-role=status]')).toContainText('Location on');
    expect(cp.url()).not.toContain('/s/'); // the link code is gone from the address

    // 10 fixes along the route, one a second; the first ones may be dropped until the assignment reaches tracking
    const points = route(10);
    for (const p of points) {
      await courierCtx.setGeolocation({ ...p, accuracy: 8 });
      await cp.waitForTimeout(1000);
    }
    for (let i = 0; i < 10; i++) {
      await courierCtx.setGeolocation({ ...points[9], accuracy: 8 });
      await cp.waitForTimeout(1000);
    }

    // pickup: under policy "any" the courier's tap alone picks up (the status ends up picked_up, maybe via at_pickup)
    const atPickup = cp.locator('[data-act="at_pickup"]');
    if (await atPickup.isVisible()) await atPickup.click();
    await cp.locator('[data-act="pickup_confirmed"]').click();
    await expect(cp.locator('article.delivery .status')).toHaveText('Picked up, on your way');
    // the route ends at the door, so tracking may already have moved on to "arriving" by the time we look
    await expect(tp.locator('[data-card="eta"] .dt-head')).toHaveText(
      new RegExp(`^(${HEAD.picked_up}|${HEAD.arriving})$`),
    );
    expect(['picked_up', 'arriving']).toContain(await status(ride.delivery.id));

    // the courier shows on the tracking page (its card; the 3D label is off screen on a small viewport), once the location reaches the customer (fixes keep coming while we wait)
    await expect(async () => {
      await courierCtx.setGeolocation({ ...points[9], accuracy: 8 });
      await expect(tp.locator('[data-card="courier"]')).toHaveCount(1, { timeout: 1500 });
    }).toPass({ timeout: 20_000 });

    // on to delivered, tolerating the optional "almost there" step
    const arriving = cp.locator('[data-act="arriving"]');
    if (await arriving.isVisible()) await arriving.click();
    await cp.locator('[data-act="delivered"]').click();
    await expect(tp.locator('.dt-card-final .dt-head')).toHaveText('Delivered');
    expect(await status(ride.delivery.id)).toBe('delivered');
  } finally {
    await courierCtx.close();
    await trackCtx.close();
  }
});

test('revoked mid-ride shows the ended screen', async ({ browser }) => {
  const ride = await createRide();
  const ctx = await browser.newContext({
    geolocation: { ...route(10)[0], accuracy: 8 },
    permissions: ['geolocation'],
    ...(test.info().project.use as object),
  });
  const page = await ctx.newPage();
  try {
    await page.goto(ride.link);
    await page.getByRole('button', { name: 'Start shift' }).click();
    await expect(page.locator('article.delivery')).toHaveCount(1);
    for (const p of route(3)) {
      await ctx.setGeolocation({ ...p, accuracy: 8 });
      await page.waitForTimeout(1000);
    }

    await revokeSession(ride.sessionId);
    // moving keeps a batch due; within one batch interval plus a request the next call answers 401
    const moving = (async () => {
      for (const p of route(10)) {
        await ctx.setGeolocation({ ...p, accuracy: 8 }).catch(() => undefined);
        await page.waitForTimeout(1000).catch(() => undefined);
      }
    })();
    await expect(page.getByRole('heading', { name: 'Your shift has ended' })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText(/updates waiting/)).toHaveCount(0); // buffered is 0
    await expect(page.locator('article.delivery')).toHaveCount(0);
    await moving;
  } finally {
    await ctx.close();
  }
});
