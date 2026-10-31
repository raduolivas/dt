import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { API, EMBED_ORIGIN } from './lib/stack';

// Once per run: a tenant for the tests (both delivery models, 1 minute of preparation, the demo origin allowed to
// frame the hosted page) and a test key, through dtctl like `make seed`. The key reaches the workers through the
// environment and is never printed.

const repo = resolve(import.meta.dirname, '../..');
const certs = resolve(repo, 'infra/compose/certs');

// dtctl's settings for the local stack (the Makefile exports the same); dev-only values, never secrets
const dtctlEnv = {
  DT_ACCOUNTS_URL: 'https://127.0.0.1:8084',
  DT_INTERNAL_RPC_TOKEN: 'dev-only-not-a-secret-internal-token-0000',
  DT_INTERNAL_RPC_TLS_CERT: resolve(certs, 'dtctl.pem'),
  DT_INTERNAL_RPC_TLS_KEY: resolve(certs, 'dtctl.key'),
  DT_INTERNAL_RPC_TLS_CA: resolve(certs, 'ca.pem'),
  ...process.env,
};

function dtctl(...args: string[]): string {
  const bin = process.env.DTCTL ?? resolve(repo, 'backend/bin/dtctl');
  try {
    return execFileSync(bin, args, {
      env: dtctlEnv,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    }).trim();
  } catch (err) {
    const e = err as { stderr?: string; message: string };
    throw new Error(
      `dtctl ${args[0]} ${args[1]} failed (is the stack up? run make dev; is dtctl built? run make dtctl): ${e.stderr || e.message}`,
    );
  }
}

export default async function globalSetup(): Promise<void> {
  const run = new Date().toISOString().replace(/\D/g, '').slice(0, 14);
  const tenant = dtctl('tenant', 'create', '--name', `E2E ${run}`);
  dtctl(
    'tenant',
    'settings',
    '--tenant',
    tenant,
    '--models',
    'direct,end_to_end',
    '--default',
    'end_to_end',
    '--pickup',
    'any',
    '--prep-minutes',
    '1',
    '--embed-origin',
    EMBED_ORIGIN,
  );
  const key = dtctl('key', 'create', '--tenant', tenant, '--env', 'test', '--name', 'e2e');
  if (!/^dt_sk_test_[0-9A-Za-z]{55}$/.test(key))
    throw new Error('dtctl key create did not print a test secret key');

  // The gateway learns new keys through NATS: wait until it accepts this one.
  const deadline = Date.now() + 15_000;
  for (;;) {
    const res = await fetch(`${API}/me`, { headers: { Authorization: `Bearer ${key}` } }).catch(() => null);
    if (res?.status === 200) break;
    if (Date.now() > deadline)
      throw new Error(
        `the gateway did not accept the new key within 15 s (last status ${res?.status ?? 'none'})`,
      );
    await new Promise((r) => setTimeout(r, 250));
  }
  process.env.DT_E2E_TENANT = tenant;
  process.env.DT_E2E_KEY = key;
  console.log(`e2e tenant ${tenant}`);
}
