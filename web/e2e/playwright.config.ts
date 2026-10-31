import { defineConfig, devices } from '@playwright/test';

// End-to-end tests of the hosted tracking page, the SDK and embedding, against the local stack:
//   make dev && make web-assets && make e2e
// The stack serves the API on :8080 (the edge proxy), the page shell on :8080/t/<token> and the page, SDK and tile
// fixture from the cdn container on :8090. serve-demo.mjs serves the SDK demo page (web/apps/sdk-demo) at
// https://localhost:5180 (an allowed embed origin), https://localhost:5181 (not allowed) and http://localhost:5182,
// and the stack behind one TLS origin at https://localhost:8443 (for the hosted page inside an https page).
// Screenshot tests (look.spec.ts, project chromium-look) and the no-WebGL test (fallback.spec.ts, project
// chromium-no-webgl) run in Chromium only.

const iphoneSE = devices['iPhone SE (3rd gen)'] ?? devices['iPhone SE'];

// Chromium's default WebGL in headless mode is SwiftShader, which draws the 3D city at about one frame a second and
// starves the page. ANGLE on the system's OpenGL uses the GPU when there is one and Mesa's (multi-threaded)
// llvmpipe otherwise.
const chromiumGL = { args: ['--use-angle=gl', '--ignore-gpu-blocklist'] };

export default defineConfig({
  testDir: './tests',
  globalSetup: './global-setup.ts',
  // a test drives a simulated delivery through every state (one to two minutes at 20x); WebKit's headless WebGL is
  // software-only and draws about a frame every two seconds, which slows every page interaction
  timeout: 420_000,
  expect: { timeout: 15_000, toHaveScreenshot: { maxDiffPixelRatio: 0.02 } },
  fullyParallel: true,
  // each browser renders the 3D city in software (no GPU): more workers than this starve the pages on 4 cores
  workers: 2,
  retries: process.env.CI ? 1 : 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  snapshotPathTemplate: '{testDir}/__screenshots__/{testFilePath}/{arg}{ext}',
  use: {
    locale: 'en-US',
    timezoneId: 'Europe/Amsterdam',
    ignoreHTTPSErrors: true, // the demo server's self-signed localhost certificate
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'node serve-demo.mjs',
    url: 'https://localhost:5180/healthz',
    ignoreHTTPSErrors: true,
    reuseExistingServer: !process.env.CI,
    stdout: 'ignore',
    stderr: 'pipe',
  },
  projects: [
    {
      name: 'iphone-se',
      testIgnore: [/look\.spec/, /fallback\.spec/],
      use: {
        browserName: 'webkit',
        userAgent: iphoneSE.userAgent,
        viewport: { width: 375, height: 667 },
        deviceScaleFactor: 2,
        isMobile: true,
        hasTouch: true,
      },
    },
    {
      name: 'iphone-14',
      testIgnore: [/look\.spec/, /fallback\.spec/],
      use: {
        browserName: 'webkit',
        userAgent: devices['iPhone 14'].userAgent,
        viewport: { width: 390, height: 844 },
        deviceScaleFactor: 3,
        isMobile: true,
        hasTouch: true,
      },
    },
    {
      name: 'desktop-chromium',
      testIgnore: [/look\.spec/, /fallback\.spec/],
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: chromiumGL,
        // A GPU-less CI runner draws 1280 × 720 in software slowly enough to starve the page (it then misses the
        // stream's 5 s auth window and resyncs past states); 1000 × 640 still gets the desktop side panel (≥ 900 px).
        ...(process.env.CI ? { viewport: { width: 1000, height: 640 } } : {}),
      },
    },
    {
      name: 'desktop-webkit',
      testIgnore: [/look\.spec/, /fallback\.spec/],
      use: { ...devices['Desktop Safari'] },
    },
    {
      name: 'desktop-firefox',
      testIgnore: [/look\.spec/, /fallback\.spec/],
      use: { ...devices['Desktop Firefox'] },
    },
    {
      // The screenshots: Chromium with its default software WebGL (SwiftShader), which draws the same pixels on every
      // machine, unlike a GPU or the system's Mesa. Slow, but the scene is still (frozen clock, reduced motion).
      name: 'chromium-look',
      testMatch: [/look\.spec/],
      use: { ...devices['Desktop Chrome'] },
    },
    {
      // Chromium without WebGL: the renderer draws its 2D fallback map
      name: 'chromium-no-webgl',
      testMatch: [/fallback\.spec/],
      use: { ...devices['Desktop Chrome'], launchOptions: { args: ['--disable-webgl', '--disable-webgl2'] } },
    },
  ],
});
