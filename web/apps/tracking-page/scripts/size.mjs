// Fails the build when the hosted page's bundle grows past its gzipped size limit in package.json ("sizeLimit",
// bytes), like the SDK's check (spec §7). The limits were set from the first build plus 10%; raise them on purpose,
// in review, not silently. A JavaScript or CSS file in dist that has no limit fails too, so a new chunk cannot
// slip past the budget.
import { readdirSync, readFileSync } from 'node:fs';
import { relative, resolve } from 'node:path';
import { gzipSync } from 'node:zlib';

const dir = resolve(import.meta.dirname, '..');
const { sizeLimit } = JSON.parse(readFileSync(resolve(dir, 'package.json'), 'utf8'));
let failed = false;
for (const [file, limit] of Object.entries(sizeLimit ?? {})) {
  const gz = gzipSync(readFileSync(resolve(dir, file)), { level: 9 }).length;
  const ok = gz <= limit;
  failed ||= !ok;
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${file}: ${(gz / 1024).toFixed(1)} KiB gzipped (limit ${(limit / 1024).toFixed(1)} KiB)`);
}
if (!sizeLimit || Object.keys(sizeLimit).length === 0) {
  console.error('no sizeLimit in package.json');
  failed = true;
}
const files = readdirSync(resolve(dir, 'dist'), { recursive: true, encoding: 'utf8' })
  .filter((f) => /\.(js|css)$/.test(f))
  .map((f) => relative(dir, resolve(dir, 'dist', f)));
for (const f of files) {
  if (!(f in (sizeLimit ?? {}))) {
    console.error(`FAIL ${f}: no size limit in package.json (add one in review)`);
    failed = true;
  }
}
process.exit(failed ? 1 : 0);
