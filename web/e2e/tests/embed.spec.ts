import { expect, test } from '@playwright/test';
import {
  EMBED_ORIGIN,
  OTHER_ORIGIN,
  createDelivery,
  ownClientIp,
  testClientIp,
  viaFront,
  waitTrackable,
} from '../lib/stack';
import { HEAD } from '../lib/view';

// frame-ancestors: the test tenant allows EMBED_ORIGIN (dtctl tenant settings --embed-origin, in the global setup).
// The embedding pages are https (embed origins must be), so the frame loads the link through the stack's TLS front:
// browsers refuse an http frame on an https page (WebKit even for localhost).

test.beforeEach(async ({ context }) => {
  await ownClientIp(context);
});

/** The shell's Content-Security-Policy for a link (the same for every embedding page). */
async function shellCsp(url: string): Promise<string> {
  const res = await fetch(url, { headers: { 'X-Forwarded-For': testClientIp() } });
  expect(res.status).toBe(200);
  return res.headers.get('content-security-policy') ?? '';
}

test('the hosted page loads in an iframe on an allowed origin', async ({ page }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  expect(await shellCsp(d.url)).toContain(`frame-ancestors ${EMBED_ORIGIN}`);
  await page.goto(`${EMBED_ORIGIN}/?frame=${encodeURIComponent(viaFront(d.url))}`);
  const frame = page.frameLocator('iframe');
  await expect(frame.locator('[data-card="eta"] .dt-head')).toHaveText(HEAD.confirmed);
  await expect(frame.locator('canvas.scene-canvas')).toHaveCount(1);
});

test('the hosted page is blocked in an iframe on another origin', async ({ page, browserName }) => {
  const d = await createDelivery('end_to_end');
  await waitTrackable(d.token);
  const console: string[] = [];
  page.on('console', (m) => console.push(m.text()));
  // the page's script is requested only by a document the browser agreed to show
  const scripts: string[] = [];
  page.on('request', (r) => {
    if (r.url().endsWith('/entry.js')) scripts.push(r.url());
  });
  // the shell names only the allowed origin as a frame ancestor
  const csp = await shellCsp(d.url);
  expect(csp).toContain(`frame-ancestors ${EMBED_ORIGIN}`);
  expect(csp).not.toContain(OTHER_ORIGIN);
  // a refused frame may never fire its load event (Firefox), so the page's own load is not awaited
  await page.goto(`${OTHER_ORIGIN}/?frame=${encodeURIComponent(viaFront(d.url))}`, {
    waitUntil: 'domcontentloaded',
  });
  await expect(page.locator('iframe')).toBeAttached();

  // the browser refuses to show it: the page never starts and no tracking view appears in any frame
  await page.waitForTimeout(5_000);
  expect(scripts).toEqual([]);
  for (const f of page.frames()) {
    // a refused frame may hold an error document that never answers (Firefox): that is no view either
    const views = await Promise.race([
      f.locator('.dt-root').count(),
      new Promise<number>((r) => setTimeout(() => r(0), 3_000)),
    ]);
    expect(views).toBe(0);
  }
  if (browserName === 'chromium') {
    // Chromium reports the violation on the embedding page's console
    expect(console.some((m) => m.includes('frame-ancestors'))).toBe(true);
  }
});
