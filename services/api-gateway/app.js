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
];

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
  ['GET', /^\/designs\/[^/]+$/], // reading a design is public; registering and matching need a token
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

// shortcut: in-memory fixed window per IP, so limits are per gateway instance; move to Redis when the gateway scales out.
function rateLimiter(limitPerMinute) {
  const hits = new Map();
  const timer = setInterval(() => hits.clear(), 60_000);
  timer.unref();
  return (req, res, next) => {
    const n = (hits.get(req.ip) || 0) + 1;
    hits.set(req.ip, n);
    if (n > limitPerMinute) return fail(res, 429, 'RATE_LIMITED', 'too many requests, slow down');
    next();
  };
}

function proxy(req, res, target, path, identity) {
  const url = new URL(path, target);
  const headers = { ...req.headers, host: url.host };
  // Identity only ever comes from the verified token, never from the client.
  delete headers['x-user-id'];
  delete headers['x-user-role'];
  delete headers.authorization;
  if (identity) {
    headers['x-user-id'] = identity.sub;
    headers['x-user-role'] = identity.role;
  }

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

function createApp({ services, secret, rateLimitPerMinute = 300 }) {
  if (!secret) throw new Error('JWT secret is required');
  const app = express();
  app.disable('x-powered-by');

  app.get('/health', (req, res) => {
    res.status(200).json({ status: 'ok', service: 'api-gateway' });
  });

  app.use('/api/v1', rateLimiter(rateLimitPerMinute));

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
    const key = serviceFor(path);
    if (!key || !services[key]) return fail(res, 404, 'NOT_FOUND', 'no such route');

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

  return app;
}

module.exports = { createApp };
