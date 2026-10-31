import { mount, type MountOptions, type TileSourceSpec } from '@dt/tracking';
import './page.css';

// The hosted tracking page (GET /t/{token} on the gateway). The shell carries no token, tenant or delivery data: the
// static config is in #dt-config and the link token is the last path segment. The token stays in memory (the SDK
// exchanges it for a viewer token) and is never logged or sent anywhere else.

interface PageConfig {
  api?: string;
  tiles?: string;
  cdn?: string;
}

function readConfig(): PageConfig {
  try {
    const c: unknown = JSON.parse(document.getElementById('dt-config')?.textContent ?? '{}');
    return c && typeof c === 'object' ? (c as PageConfig) : {};
  } catch {
    return {};
  }
}

/** A `.pmtiles` archive, else a TileJSON URL; unset: the SDK's default. */
function tileSource(url: string | undefined): TileSourceSpec | undefined {
  if (!url) return undefined;
  return new URL(url, location.href).pathname.endsWith('.pmtiles')
    ? { kind: 'pmtiles', url }
    : { kind: 'zxy', tilejson: url };
}

/** `/t/<token>` → the token; anything else → '' (the view shows the inactive card). */
function tokenFromPath(path: string): string {
  const m = /^\/t\/([A-Za-z0-9_-]+)\/?$/.exec(path);
  return m ? m[1] : '';
}

const config = readConfig();
const options: MountOptions = {
  token: tokenFromPath(location.pathname),
  api: config.api || undefined,
  tiles: tileSource(config.tiles),
};

// Test hook, only in builds made with VITE_DT_TEST_HOOKS=1 (the local CDN; never staging or production, where this
// branch is removed at build time): `?dt-clock=<ms>` freezes the renderer's clock for the screenshot tests.
if (import.meta.env.VITE_DT_TEST_HOOKS === '1') {
  const frozen = Number(new URLSearchParams(location.search).get('dt-clock'));
  if (location.search.includes('dt-clock=') && Number.isFinite(frozen)) options.clock = () => frozen;
}

mount(document.getElementById('root') ?? document.body, options);
