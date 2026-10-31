import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { ORIGIN, ownClientIp } from '../lib/stack';

// Screenshots of the hosted page against committed baselines (Chromium only: the chromium-look project). The shell,
// the page, the SDK and the tile fixture are the real ones from the stack and the cdn container; the viewer API
// answers with fixed snapshots (recorded from the stack's simulator, web/e2e/fixtures/look.json), the renderer's
// clock is frozen (?dt-clock, a test hook of the local dev build), the page's Date is fixed and motion is reduced,
// so a picture only changes when the look does. Update the baselines with: make e2e E2E_ARGS=--update-snapshots

interface LookFixture {
  now: string;
  preparing: unknown;
  live: unknown;
}
const fixture = JSON.parse(
  readFileSync(resolve(import.meta.dirname, '../fixtures/look.json'), 'utf8'),
) as LookFixture;

// any 22-character link token: the session is answered below; the shell is the same for every token
const TOKEN = 'e2eLookFixtureToken000';
const CORS = {
  'Access-Control-Allow-Origin': '*',
  'Access-Control-Expose-Headers': 'Retry-After, Request-Id',
};

async function serveSnapshot(page: Page, snapshot: unknown): Promise<void> {
  const expiresAt = new Date(Date.parse(fixture.now) + 3_600_000).toISOString();
  await page.route(/^http:\/\/localhost:8080\/v1\/tracking\/(session|snapshot)$/, (route) => {
    if (route.request().method() === 'OPTIONS') {
      return route.fulfill({
        status: 204,
        headers: {
          ...CORS,
          'Access-Control-Allow-Methods': 'POST, GET, OPTIONS',
          'Access-Control-Allow-Headers': 'Authorization, Content-Type',
        },
      });
    }
    const body = route.request().url().endsWith('/session')
      ? { viewer_token: 'e2e-look-viewer-token', expires_at: expiresAt, snapshot, branding: null }
      : snapshot;
    return route.fulfill({
      status: 200,
      headers: { ...CORS, 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  });
  // an open stream that stays quiet: the page keeps the snapshot it has
  await page.routeWebSocket('ws://localhost:8080/v1/tracking/stream', () => {});
}

const VIEWPORTS = {
  'iphone-se': { width: 375, height: 667 },
  'iphone-14': { width: 390, height: 844 },
  desktop: { width: 1280, height: 720 },
} as const;

test.use({ reducedMotion: 'reduce' });
// one at a time: software WebGL is slow, and a starved page builds its city late
test.describe.configure({ mode: 'serial' });

const CASES = [
  ...(['light', 'dark'] as const).flatMap((scheme) =>
    (['preparing', 'live'] as const).map((state) => ({ scheme, state, mode: '3d' as const })),
  ),
  // the same scene seen straight down
  { scheme: 'light' as const, state: 'live' as const, mode: '2d' as const },
];

for (const [device, viewport] of Object.entries(VIEWPORTS)) {
  for (const { scheme, state, mode } of CASES) {
    const name = `${device}-${scheme === 'light' ? 'day' : 'night'}-${state}${mode === '2d' ? '-2d' : ''}`;
    test(name, async ({ page, context }) => {
      await ownClientIp(context);
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
      await page.clock.setFixedTime(new Date(fixture.now));
      await serveSnapshot(page, fixture[state]);
      await page.goto(`${ORIGIN}/t/${TOKEN}?dt-clock=1000`);
      await expect(page.locator('[data-card="eta"] .dt-head')).toBeVisible();
      await expect(page.locator('.dt-root')).toHaveAttribute('data-theme', scheme);
      if (mode === '2d') {
        await page.locator('.dt-mode').click();
        await expect(page.locator('.dt-mode')).toHaveText('3D');
      }
      // the tile fixture is fetched, decoded in the worker and built into the city; the store and home labels show
      // once the city is there
      await expect(page.locator('.scene-label.home')).toBeVisible({ timeout: 120_000 });
      await expect(page.locator('.scene-label.store')).toBeVisible();
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(3_000);
      await expect(page).toHaveScreenshot(`${name}.png`, { timeout: 120_000 });
    });
  }
}
