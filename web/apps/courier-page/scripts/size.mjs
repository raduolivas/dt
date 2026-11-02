// Fails the build when the hosted page's bundle grows past its gzipped size limit in package.json ("sizeLimit", bytes),
// like tracking-page's check. Keys are file patterns where `*` stands for the content hash. The limits were set from
// the first build plus 10%; raise them on purpose, in review, not silently. A JavaScript or CSS file in dist that no
// pattern covers fails too, so a new chunk cannot slip past the budget.
import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gzipSync } from 'node:zlib';

const dir = resolve(import.meta.dirname, '..');
const { sizeLimit } = JSON.parse(readFileSync(resolve(dir, 'package.json'), 'utf8'));
const patterns = Object.entries(sizeLimit ?? {}).map(([p, limit]) => [
  p,
  limit,
  new RegExp('^' + p.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '[^/]+') + '$'),
]);
let failed = false;
if (patterns.length === 0) {
  console.error('no sizeLimit in package.json');
  failed = true;
}
const files = readdirSync(resolve(dir, 'dist'), { recursive: true, encoding: 'utf8' })
  .map((f) => f.split('\\').join('/'))
  .filter((f) => /\.(js|css)$/.test(f));
for (const [p, , re] of patterns) {
  if (!files.some((f) => re.test(f))) {
    console.error(`FAIL ${p}: no such file in dist`);
    failed = true;
  }
}
for (const f of files) {
  const hit = patterns.find(([, , re]) => re.test(f));
  if (!hit) {
    console.error(`FAIL ${f}: no size limit in package.json (add one in review)`);
    failed = true;
    continue;
  }
  const gz = gzipSync(readFileSync(resolve(dir, 'dist', f)), { level: 9 }).length;
  const ok = gz <= hit[1];
  failed ||= !ok;
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${f}: ${(gz / 1024).toFixed(1)} KiB gzipped (limit ${(hit[1] / 1024).toFixed(1)} KiB)`);
}
process.exit(failed ? 1 : 0);
