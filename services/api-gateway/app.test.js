const { test, before, after } = require('node:test');
const assert = require('node:assert');
const http = require('http');
const jwt = require('jsonwebtoken');
const { createApp } = require('./app');

const SECRET = 'test-secret';
let upstream, gateway, base, upstreamURL;

function listen(server) {
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));
}

const token = (claims, opts = { expiresIn: '5m' }) => jwt.sign(claims, SECRET, opts);
const access = token({ sub: 'user-1', role: 'wholesaler', typ: 'access' });

before(async () => {
  // Echo server standing in for every downstream service.
  upstream = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      res.writeHead(201, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ method: req.method, url: req.url, headers: req.headers, body }));
    });
  });
  // Raw upgrade: reply 101, send what we received as the first line, then echo bytes. Thread "forbidden" refuses.
  upstream.on('upgrade', (req, socket) => {
    if (req.url.includes('forbidden')) return socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n');
    socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n');
    socket.write(JSON.stringify({ url: req.url, headers: req.headers }) + '\n');
    socket.pipe(socket);
  });
  upstreamURL = `http://127.0.0.1:${await listen(upstream)}`;
  const services = { auth: upstreamURL, profile: upstreamURL, catalog: upstreamURL, media: upstreamURL, order: 'http://127.0.0.1:1', ledger: upstreamURL, qr: upstreamURL, counterfeit: upstreamURL, search: upstreamURL, inventory: upstreamURL, analytics: upstreamURL, notifications: upstreamURL, chat: upstreamURL };
  const app = createApp({ services, secret: SECRET, rateLimitPerMinute: 1000 });
  gateway = http.createServer(app);
  gateway.on('upgrade', app.upgrade);
  base = `http://127.0.0.1:${await listen(gateway)}`;
});

after(() => {
  upstream.close();
  gateway.close();
});

const get = (path, headers = {}) => fetch(base + path, { headers });

test('public route proxies with path rewrite, query string and body', async () => {
  const r = await fetch(`${base}/api/v1/auth/login?x=1`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{"email":"a@b.com"}',
  });
  assert.equal(r.status, 201);
  const echo = await r.json();
  assert.equal(echo.url, '/auth/login?x=1');
  assert.equal(echo.body, '{"email":"a@b.com"}');
  assert.equal(echo.headers['x-user-id'], undefined);
});

test('protected route without token is rejected', async () => {
  const r = await get('/api/v1/auth/me');
  assert.equal(r.status, 401);
  assert.equal((await r.json()).error.code, 'UNAUTHENTICATED');
});

test('valid token forwards identity and drops the bearer token', async () => {
  const r = await get('/api/v1/auth/me', { authorization: `Bearer ${access}` });
  const echo = await r.json();
  assert.equal(echo.headers['x-user-id'], 'user-1');
  assert.equal(echo.headers['x-user-role'], 'wholesaler');
  assert.equal(echo.headers.authorization, undefined);
});

test('client-supplied identity headers are stripped', async () => {
  const forged = { 'x-user-id': 'admin-id', 'x-user-role': 'admin' };
  let echo = await (await get('/api/v1/products', forged)).json();
  assert.equal(echo.headers['x-user-id'], undefined);
  assert.equal(echo.headers['x-user-role'], undefined);

  echo = await (await get('/api/v1/auth/me', { ...forged, authorization: `Bearer ${access}` })).json();
  assert.equal(echo.headers['x-user-id'], 'user-1');
  assert.equal(echo.headers['x-user-role'], 'wholesaler');
});

test('invalid, expired, wrong-type and wrong-algorithm tokens are rejected', async () => {
  const bad = [
    'Bearer not-a-jwt',
    `Bearer ${jwt.sign({ sub: 'u', role: 'x', typ: 'access' }, 'other-secret')}`,
    `Bearer ${token({ sub: 'u', role: 'x', typ: 'access' }, { expiresIn: '-1m' })}`,
    `Bearer ${token({ sub: 'u', role: 'x', typ: 'refresh' })}`,
    `Bearer ${jwt.sign({ sub: 'u', role: 'admin', typ: 'access' }, null, { algorithm: 'none' })}`,
    `Basic ${access}`,
  ];
  for (const authorization of bad) {
    const r = await get('/api/v1/auth/me', { authorization });
    assert.equal(r.status, 401, authorization);
    assert.equal((await r.json()).error.code, 'INVALID_TOKEN');
  }
});

test('public GET is open but writes need a token', async () => {
  assert.equal((await get('/api/v1/products/5')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/products`, { method: 'POST' })).status, 401);
  assert.equal((await fetch(`${base}/api/v1/payments/webhook`, { method: 'POST' })).status, 502); // public, order upstream down
  assert.equal((await get('/api/v1/pieces/p1/verify')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/pieces/p1/handoffs`, { method: 'POST' })).status, 401);
  assert.equal((await get('/api/v1/qr/abc/resolve')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/qr/abc/scan-log`, { method: 'POST' })).status, 201);
  assert.equal((await fetch(`${base}/api/v1/qr/p1/generate`, { method: 'POST' })).status, 401);
  assert.equal((await get('/api/v1/designs/d1')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/designs`, { method: 'POST' })).status, 401);
  assert.equal((await fetch(`${base}/api/v1/designs/match`, { method: 'POST' })).status, 401);
  assert.equal((await get('/api/v1/designs')).status, 201);
  assert.equal((await get('/api/v1/search?q=silk')).status, 201);
  assert.equal((await get('/api/v1/search/suggest?q=si')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/search/reindex`, { method: 'POST' })).status, 401);
  assert.equal((await get('/api/v1/inventory/5')).status, 201);
  assert.equal((await fetch(`${base}/api/v1/inventory/5`, { method: 'PATCH' })).status, 401);
  assert.equal((await get('/api/v1/analytics/trends')).status, 201);
  assert.equal((await get('/api/v1/analytics/seller/w1/summary')).status, 401);
});

test('internal inventory endpoints are not routed, even with a token', async () => {
  for (const path of ['/api/v1/inventory/reserve', '/api/v1/inventory/release', '/api/v1/inventory/reserve/']) {
    const r = await fetch(`${base}${path}`, { method: 'POST', headers: { authorization: `Bearer ${access}` } });
    assert.equal(r.status, 404, path);
  }
});

test('unknown route is 404 and an unreachable service is 502', async () => {
  assert.equal((await get('/api/v1/nope')).status, 404);
  assert.equal((await get('/api/v1/authx')).status, 404);
  const r = await get('/api/v1/orders', { authorization: `Bearer ${access}` });
  assert.equal(r.status, 502);
  assert.equal((await r.json()).error.code, 'UPSTREAM_UNAVAILABLE');
});

test('aggregate health reports each service', async () => {
  const r = await get('/api/v1/health');
  const body = await r.json();
  assert.equal(r.status, 503); // order is unreachable
  assert.equal(body.data.services.order, 'down');
});

test('rate limit returns 429', async () => {
  const app = createApp({ services: { auth: upstreamURL }, secret: SECRET, rateLimitPerMinute: 2 });
  const server = http.createServer(app);
  const port = await listen(server);
  const codes = [];
  for (let i = 0; i < 3; i++) codes.push((await fetch(`http://127.0.0.1:${port}/api/v1/products`)).status);
  server.close();
  assert.deepEqual(codes, [404, 404, 429]);
});

// Opens a WebSocket-style upgrade through the gateway. Resolves {status} on refusal or {socket, first} on success.
function upgrade(path, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request(base + path, { headers: { connection: 'Upgrade', upgrade: 'websocket', ...headers } });
    req.on('response', (res) => resolve({ status: res.statusCode }));
    req.on('upgrade', (res, socket, head) => {
      // Bytes sent right after the 101 may already be in `head`.
      const done = (d) => resolve({ status: res.statusCode, socket, first: JSON.parse(d.toString().split('\n')[0]) });
      head.length ? done(head) : socket.once('data', done);
    });
    req.on('error', reject);
    req.end();
  });
}

test('websocket upgrade without a token is rejected', async () => {
  assert.equal((await upgrade('/api/v1/ws/notifications')).status, 401);
});

test('websocket upgrade with an invalid token is rejected', async () => {
  assert.equal((await upgrade('/api/v1/ws/notifications?access_token=nope')).status, 401);
});

test('websocket upgrade on an unknown path is 404', async () => {
  assert.equal((await upgrade('/api/v1/ws/other', { authorization: `Bearer ${access}` })).status, 404);
  assert.equal((await upgrade('/api/v1/ws/chat', { authorization: `Bearer ${access}` })).status, 404);
});

test('websocket upgrade forwards identity, strips the token and pipes both ways', async () => {
  const { status, socket, first } = await upgrade(`/api/v1/ws/chat/t1?access_token=${access}&x=1`, { 'x-user-id': 'spoofed' });
  assert.equal(status, 101);
  assert.equal(first.url, '/ws/chat/t1?x=1');
  assert.equal(first.headers['x-user-id'], 'user-1');
  assert.equal(first.headers['x-user-role'], 'wholesaler');
  assert.equal(first.headers.authorization, undefined);
  const echoed = new Promise((resolve) => socket.once('data', (d) => resolve(d.toString())));
  socket.write('ping');
  assert.equal(await echoed, 'ping');
  socket.destroy();
});

test('websocket upgrade refused by the service passes its status through', async () => {
  assert.equal((await upgrade('/api/v1/ws/chat/forbidden', { authorization: `Bearer ${access}` })).status, 403);
});
