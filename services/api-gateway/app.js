const express = require('express');
const http = require('http');
const jwt = require('jsonwebtoken');

// Path prefix (after /api/v1) → owning service key.
const ROUTES = [
  ['/auth', 'auth'],
  ['/users', 'auth'],
  ['/profiles', 'profile'],
  ['/products', 'catalog'],
  ['/categories', 'catalog'],
  ['/media', 'media'],
  ['/orders', 'order'],
  ['/payments', 'order'],
  ['/pieces', 'ledger'],
  ['/qr', 'qr'],
  ['/designs', 'counterfeit'],
  ['/search', 'search'],
  ['/inventory', 'inventory'],
  ['/analytics', 'analytics'],
  ['/notifications', 'notifications'],
  ['/chat', 'chat'],
  ['/reviews', 'reviews'],
  ['/trust-score', 'reviews'],
];

// WebSocket paths (after /api/v1) → owning service key. Always need a token.
const WS_ROUTES = [
  [/^\/ws\/notifications$/, 'notifications'],
  [/^\/ws\/chat\/[^/]+$/, 'chat'],
];

// Service-to-service endpoints that must never be reachable from outside.
const INTERNAL = [/^\/inventory\/(reserve|release)\/?$/];

// Routes reachable without a token. Everything else needs a valid access token.
const PUBLIC = [
  ['POST', /^\/auth\/(signup|login|refresh)$/],
  ['GET', /^\/profiles(\/|$)/],
  ['GET', /^\/products(\/|$)/],
  ['GET', /^\/categories(\/|$)/],
  ['GET', /^\/media\/[^/]+(\/content)?$/],
  ['GET', /^\/pieces(\/|$)/], // piece, history and on-chain verify are public by design
  ['GET', /^\/qr\/[^/]+\/resolve$/], // anyone scanning a tag, no login
  ['POST', /^\/qr\/[^/]+\/scan-log$/],
  ['GET', /^\/designs(\/[^/]+)?$/], // reading designs is public; registering and matching need a token
  ['GET', /^\/search(\/suggest)?$/],
  ['GET', /^\/inventory\/[^/]+$/],
  ['GET', /^\/analytics\/trends$/],
  ['GET', /^\/reviews$/],
  ['GET', /^\/trust-score\/[^/]+$/],
  ['POST', /^\/payments\/webhook$/], // verified by Razorpay's signature, not a JWT
];

const UPSTREAM_TIMEOUT_MS = 30_000;

function fail(res, status, code, message) {
  res.status(status).json({ data: null, error: { code, message }, meta: {} });
}

function isPublic(method, path) {
  return PUBLIC.some(([m, re]) => m === method && re.test(path));
}

function serviceFor(path) {
  const hit = ROUTES.find(([prefix]) => path === prefix || path.startsWith(prefix + '/'));
  return hit && hit[1];
}

// Returns {sub, role} for a valid access token, null when absent, or throws when invalid.
function identityFrom(header, secret) {
  if (!header) return null;
  const [scheme, token] = header.split(' ');
  if (scheme !== 'Bearer' || !token) throw new Error('malformed authorization header');
  const claims = jwt.verify(token, secret, { algorithms: ['HS256'] });
  if (claims.typ !== 'access' || !claims.sub) throw new Error('not an access token');
  return { sub: claims.sub, role: claims.role || '' };
}

// Hit counters for a fixed one-minute window: count(ip) resolves to the hits so far this minute, this one included.
// In memory the limit is per gateway instance; Redis shares it across instances.
function memoryCounter() {
  const hits = new Map();
  const timer = setInterval(() => hits.clear(), 60_000);
  timer.unref();
  return async (ip) => {
    const n = (hits.get(ip) || 0) + 1;
    hits.set(ip, n);
    return n;
  };
}

function redisCounter(redis) {
  return async (ip) => {
    const key = `ratelimit:${ip}:${Math.floor(Date.now() / 60_000)}`;
    const [[err, n]] = await redis.multi().incr(key).expire(key, 60).exec();
    if (err) throw err;
    return n;
  };
}

function rateLimiter(limitPerMinute, count) {
  const allow = async (ip) => {
    try {
      return (await count(ip)) <= limitPerMinute;
    } catch (err) {
      // Fail open: an unreachable Redis must not take the whole API down with it.
      console.error('rate limiter unavailable, allowing request:', err.message);
      return true;
    }
  };
  const limit = (req, res, next) => {
    allow(req.ip).then((ok) => (ok ? next() : fail(res, 429, 'RATE_LIMITED', 'too many requests, slow down')));
  };
  limit.allow = allow;
  return limit;
}

function forwardHeaders(req, url, identity) {
  const headers = { ...req.headers, host: url.host };
  // Identity only ever comes from the verified token, never from the client.
  delete headers['x-user-id'];
  delete headers['x-user-role'];
  delete headers.authorization;
  if (identity) {
    headers['x-user-id'] = identity.sub;
    headers['x-user-role'] = identity.role;
  }
  return headers;
}

function proxy(req, res, target, path, identity) {
  const url = new URL(path, target);
  const headers = forwardHeaders(req, url, identity);

  const upstream = http.request(url, { method: req.method, headers }, (up) => {
    res.writeHead(up.statusCode, up.headers);
    up.pipe(res);
  });
  upstream.setTimeout(UPSTREAM_TIMEOUT_MS, () => upstream.destroy(new Error('timeout')));
  upstream.on('error', (err) => {
    if (res.headersSent) return res.destroy();
    if (err.message === 'timeout') return fail(res, 504, 'UPSTREAM_TIMEOUT', 'service took too long to respond');
    fail(res, 502, 'UPSTREAM_UNAVAILABLE', 'service is unavailable');
  });
  req.pipe(upstream);
}

async function checkHealth(url) {
  try {
    const r = await fetch(`${url}/health`, { signal: AbortSignal.timeout(2000) });
    return r.ok ? 'ok' : 'down';
  } catch {
    return 'down';
  }
}

function createApp({ services, secret, rateLimitPerMinute = 300, countHits = memoryCounter() }) {
  if (!secret) throw new Error('JWT secret is required');
  const app = express();
  app.disable('x-powered-by');

  app.get('/health', (req, res) => {
    res.status(200).json({ status: 'ok', service: 'api-gateway' });
  });

  const limiter = rateLimiter(rateLimitPerMinute, countHits);
  app.use('/api/v1', limiter);

  app.get('/api/v1/health', async (req, res) => {
    const names = Object.keys(services);
    const states = await Promise.all(names.map((n) => checkHealth(services[n])));
    const status = Object.fromEntries(names.map((n, i) => [n, states[i]]));
    const healthy = states.every((s) => s === 'ok');
    res.status(healthy ? 200 : 503).json({ data: { status: healthy ? 'ok' : 'degraded', services: status }, error: null, meta: {} });
  });

  // No body parsing here: bodies (including multipart uploads) stream straight through.
  app.use('/api/v1', (req, res) => {
    const path = req.path;
    // Upstream URLs resolve dot segments (/x/../, %2e%2e, backslashes), so a path that changes under
    // normalisation could slip past INTERNAL and PUBLIC; refuse it outright.
    if (new URL(path, 'http://gateway').pathname !== path) return fail(res, 404, 'NOT_FOUND', 'no such route');
    const key = serviceFor(path);
    if (!key || !services[key] || INTERNAL.some((re) => re.test(path))) return fail(res, 404, 'NOT_FOUND', 'no such route');

    let identity;
    try {
      identity = identityFrom(req.headers.authorization, secret);
    } catch {
      return fail(res, 401, 'INVALID_TOKEN', 'access token is invalid or expired');
    }
    if (!identity && !isPublic(req.method, path)) {
      return fail(res, 401, 'UNAUTHENTICATED', 'authentication required');
    }
    // req.url keeps the query string and is already relative to /api/v1.
    proxy(req, res, services[key], req.url, identity);
  });

  // For server.on('upgrade'). Browsers cannot set headers on a WebSocket, so the token may come as ?access_token=.
  app.upgrade = async (req, socket, head) => {
    const reject = (status, text) => socket.end(`HTTP/1.1 ${status} ${text}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`);
    socket.on('error', () => socket.destroy());
    const url = new URL(req.url, 'http://gateway');
    const path = url.pathname.startsWith('/api/v1/') ? url.pathname.slice('/api/v1'.length) : '';
    const hit = WS_ROUTES.find(([re]) => re.test(path));
    if (!hit || !services[hit[1]]) return reject(404, 'Not Found');
    if (!(await limiter.allow(socket.remoteAddress))) return reject(429, 'Too Many Requests');

    const token = url.searchParams.get('access_token');
    url.searchParams.delete('access_token'); // keep the token out of upstream URLs and logs
    let identity;
    try {
      identity = identityFrom(req.headers.authorization || (token ? `Bearer ${token}` : undefined), secret);
    } catch {
      return reject(401, 'Unauthorized');
    }
    if (!identity) return reject(401, 'Unauthorized');

    const target = new URL(path + url.search, services[hit[1]]);
    const upstream = http.request(target, { method: 'GET', headers: forwardHeaders(req, target, identity) });
    upstream.on('upgrade', (upRes, upSocket, upHead) => {
      const lines = [];
      for (let i = 0; i < upRes.rawHeaders.length; i += 2) lines.push(`${upRes.rawHeaders[i]}: ${upRes.rawHeaders[i + 1]}`);
      socket.write(`HTTP/1.1 101 Switching Protocols\r\n${lines.join('\r\n')}\r\n\r\n`);
      if (upHead.length) socket.write(upHead);
      if (head.length) upSocket.write(head);
      upSocket.on('error', () => socket.destroy());
      socket.on('close', () => upSocket.destroy());
      upSocket.on('close', () => socket.destroy());
      upSocket.pipe(socket).pipe(upSocket);
    });
    // The service answered without upgrading (e.g. 403 for a non-participant): pass its status on.
    upstream.on('response', (upRes) => {
      upRes.resume();
      reject(upRes.statusCode, upRes.statusMessage || 'Error');
    });
    upstream.on('error', () => reject(502, 'Bad Gateway'));
    upstream.end();
  };

  return app;
}

module.exports = { createApp, redisCounter };
