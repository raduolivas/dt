import { expect, test } from '@playwright/test';
import { createDelivery, ownClientIp, waitTrackable } from '../lib/stack';
import { HEAD } from '../lib/view';

// Runs in the chromium-no-webgl project (Chromium started with --disable-webgl --disable-webgl2).

test.beforeEach(async ({ context }) => {
  await ownClientIp(context);
});

test('without WebGL the 2D fallback map and the DOM cards show', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  await page.goto(d.url);

  const webgl = await page.evaluate(() => {
    const c = document.createElement('canvas');
    return c.getContext('webgl2') !== null || c.getContext('webgl') !== null;
  });
  expect(webgl, 'this project must run without WebGL').toBe(false);

  await expect(page.locator('canvas.dt-fallback')).toBeVisible();
  await expect(page.locator('canvas.scene-canvas')).toHaveCount(1);
  await expect(page.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);
  await expect(page.locator('[data-card="route"]')).toBeAttached();
  await expect(page.locator('.dt-attribution')).toHaveText('© OpenStreetMap contributors · © OpenMapTiles');
});
