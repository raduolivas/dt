// Fails the build when a bundle grows past its gzipped size limit in package.json ("sizeLimit", bytes).
// The limits were set from the first build plus 10%; raise them on purpose, in review, not silently.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
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
process.exit(failed ? 1 : 0);
