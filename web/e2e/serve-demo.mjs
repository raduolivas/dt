// Test servers for the end-to-end tests (Playwright starts this file; see playwright.config.ts).
//
// The SDK demo page (web/apps/sdk-demo) at
//   https://localhost:5180  the origin the test tenant allows to frame the hosted page (embed origins are https-only)
//   https://localhost:5181  an origin it does not allow
//   http://localhost:5182   a plain-http partner page, for the SDK test: an https page may not load the local stack's
//                           http URLs in WebKit (mixed content, even for localhost)
// and an HTTPS front for the local stack at
//   https://localhost:8443  the edge (:8080) and the CDN (:8090) behind one TLS origin, like staging. The hosted page
//                           shell is rewritten to name it instead of the http URLs, so the page works inside an https
//                           embedding page in every browser. Its frame-ancestors are passed through unchanged.
// The certificate is a throwaway self-signed one for localhost, made with openssl into build/e2e/ (the tests ignore
// certificate errors).
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { request, createServer as createHttpServer } from 'node:http';
import { createServer } from 'node:https';
import { connect } from 'node:net';
import { resolve } from 'node:path';

const repo = resolve(import.meta.dirname, '../..');
const dir = resolve(repo, 'build/e2e');
const key = resolve(dir, 'demo.key');
const cert = resolve(dir, 'demo.pem');
if (!existsSync(key) || !existsSync(cert)) {
  mkdirSync(dir, { recursive: true });
  execFileSync(
    'openssl',
    [
      'req',
      '-x509',
      '-newkey',
      'ec',
      '-pkeyopt',
      'ec_paramgen_curve:prime256v1',
      '-nodes',
      '-days',
      '30',
      '-subj',
      '/CN=localhost',
      '-addext',
      'subjectAltName=DNS:localhost,IP:127.0.0.1',
      '-keyout',
      key,
      '-out',
      cert,
    ],
    { stdio: 'ignore' },
  );
}
const tls = { key: readFileSync(key), cert: readFileSync(cert) };

// ---- the demo page ----

const page = resolve(repo, 'web/apps/sdk-demo/index.html');
const demo = (req, res) => {
  const path = new URL(req.url ?? '/', 'https://localhost').pathname;
  if (path === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'text/plain' }).end('ok');
  } else if (path === '/' || path === '/index.html') {
    // read on every request, so an edited demo page needs no restart
    res
      .writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' })
      .end(readFileSync(page));
  } else {
    res.writeHead(404, { 'Content-Type': 'text/plain' }).end('not found');
  }
};

// ---- the HTTPS front ----

const FRONT = 'https://localhost:8443';
const EDGE = { host: '127.0.0.1', port: 8080 };
const CDN = { host: '127.0.0.1', port: 8090 };
const toFront = (s) =>
  s
    .replaceAll('ws://localhost:8080', 'wss://localhost:8443')
    .replaceAll(/http:\/\/localhost:80[89]0/g, FRONT);
const upstream = (path) => (/^\/(page|sdk|tiles)\//.test(path) ? CDN : EDGE);

const front = (req, res) => {
  const to = upstream(req.url ?? '/');
  const up = request(
    { ...to, method: req.method, path: req.url, headers: { ...req.headers, host: `localhost:${to.port}` } },
    (r) => {
      const headers = { ...r.headers };
      if (headers['content-security-policy'])
        headers['content-security-policy'] = toFront(headers['content-security-policy']);
      if (!/^\/t\//.test(req.url ?? '')) {
        res.writeHead(r.statusCode ?? 502, headers);
        r.pipe(res); // streams (SSE) pass through as they come
        return;
      }
      const chunks = [];
      r.on('data', (c) => chunks.push(c));
      r.on('end', () => {
        const body = toFront(Buffer.concat(chunks).toString('utf8'));
        delete headers['content-length'];
        res.writeHead(r.statusCode ?? 502, headers).end(body);
      });
    },
  );
  up.on('error', () => res.writeHead(502).end());
  req.pipe(up);
};

/** WebSocket upgrades (the viewer stream) go to the edge as raw bytes. */
const upgrade = (req, socket, head) => {
  const up = connect(EDGE.port, EDGE.host, () => {
    const lines = [`${req.method} ${req.url} HTTP/1.1`];
    for (let i = 0; i < req.rawHeaders.length; i += 2) {
      const name = req.rawHeaders[i];
      lines.push(
        `${name}: ${name.toLowerCase() === 'host' ? `localhost:${EDGE.port}` : req.rawHeaders[i + 1]}`,
      );
    }
    up.write(lines.join('\r\n') + '\r\n\r\n');
    if (head.length) up.write(head);
    socket.pipe(up).pipe(socket);
  });
  const close = () => {
    socket.destroy();
    up.destroy();
  };
  up.on('error', close);
  socket.on('error', close);
};

// loopback only, on both address families (browsers may resolve localhost to either)
const listen = (make, port) => {
  for (const host of ['127.0.0.1', '::1']) make().listen(port, host);
  console.log(`listening on localhost:${port}`);
};
listen(() => createServer(tls, demo), 5180);
listen(() => createServer(tls, demo), 5181);
listen(() => createHttpServer(demo), 5182);
listen(() => createServer(tls, front).on('upgrade', upgrade), 8443);
