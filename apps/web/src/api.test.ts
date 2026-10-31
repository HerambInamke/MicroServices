import { beforeEach, expect, test, vi } from 'vitest';

type Call = { method: string; path: string; auth?: string; body?: any };
let calls: Call[];
let handler: (c: Call) => [number, unknown];

const ok = (data: unknown): [number, unknown] => [200, { data, error: null, meta: {} }];
const fail = (status: number, code: string): [number, unknown] => [status, { data: null, error: { code, message: code }, meta: {} }];

// Fresh module (and so a fresh session cache) per test, with in-memory localStorage and a scripted fetch.
async function load() {
  vi.resetModules();
  const store = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => store.set(k, v),
    removeItem: (k: string) => store.delete(k),
  });
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    const call: Call = {
      method: init.method!,
      path: url.replace('/api/v1', ''),
      auth: (init.headers as Record<string, string>).authorization,
      body: init.body ? JSON.parse(init.body as string) : undefined,
    };
    calls.push(call);
    const [status, json] = handler(call);
    return new Response(JSON.stringify(json), { status });
  });
  return import('./api');
}

beforeEach(() => {
  calls = [];
});

async function loggedIn() {
  const mod = await load();
  handler = (c) => (c.path === '/auth/login' ? ok({ accessToken: 'a1', refreshToken: 'r1' }) : ok({ id: 'u1', email: 'x@y.z', role: 'retailer' }));
  await mod.login('x@y.z', 'password123');
  calls = [];
  return mod;
}

test('login stores the tokens and the user from /auth/me', async () => {
  const mod = await load();
  handler = (c) => (c.path === '/auth/login' ? ok({ accessToken: 'a1', refreshToken: 'r1' }) : ok({ id: 'u1', email: 'x@y.z', role: 'retailer' }));
  await mod.login('x@y.z', 'password123');
  expect(calls[1]).toMatchObject({ method: 'GET', path: '/auth/me', auth: 'Bearer a1' });
  expect(mod.getSession()).toEqual({ accessToken: 'a1', refreshToken: 'r1', user: { id: 'u1', email: 'x@y.z', role: 'retailer' } });
  expect(JSON.parse(localStorage.getItem('session')!).accessToken).toBe('a1');
});

test('sends the bearer token and unwraps data', async () => {
  const mod = await loggedIn();
  handler = () => ok([{ id: 1 }]);
  expect(await mod.api('GET', '/orders')).toEqual([{ id: 1 }]);
  expect(calls).toEqual([{ method: 'GET', path: '/orders', auth: 'Bearer a1', body: undefined }]);
});

test('anonymous calls send no authorization header', async () => {
  const mod = await load();
  handler = () => ok([]);
  await mod.api('GET', '/products');
  expect(calls[0].auth).toBeUndefined();
});

test('error envelopes become ApiError with status and code', async () => {
  const mod = await loggedIn();
  handler = () => fail(422, 'BELOW_MOQ');
  await expect(mod.api('POST', '/orders', {})).rejects.toMatchObject({ status: 422, code: 'BELOW_MOQ' });
});

test('a 401 refreshes once, retries with the new token, and keeps the rotated pair', async () => {
  const mod = await loggedIn();
  handler = (c) => {
    if (c.path === '/auth/refresh') return ok({ accessToken: 'a2', refreshToken: 'r2' });
    return c.auth === 'Bearer a2' ? ok('fresh') : fail(401, 'UNAUTHORIZED');
  };
  expect(await mod.api('GET', '/orders')).toBe('fresh');
  expect(calls.map((c) => c.path)).toEqual(['/orders', '/auth/refresh', '/orders']);
  expect(calls[1].body).toEqual({ refreshToken: 'r1' });
  expect(mod.getSession()).toMatchObject({ accessToken: 'a2', refreshToken: 'r2', user: { id: 'u1' } });
});

test('parallel 401s share a single refresh (the refresh token is single-use)', async () => {
  const mod = await loggedIn();
  handler = (c) => {
    if (c.path === '/auth/refresh') return ok({ accessToken: 'a2', refreshToken: 'r2' });
    return c.auth === 'Bearer a2' ? ok('fresh') : fail(401, 'UNAUTHORIZED');
  };
  await Promise.all([mod.api('GET', '/orders'), mod.api('GET', '/notifications')]);
  expect(calls.filter((c) => c.path === '/auth/refresh')).toHaveLength(1);
});

test('a failed refresh logs the user out and surfaces the 401', async () => {
  const mod = await loggedIn();
  const seen = vi.fn();
  mod.subscribe(seen);
  handler = (c) => (c.path === '/auth/refresh' ? fail(401, 'INVALID_TOKEN') : fail(401, 'UNAUTHORIZED'));
  await expect(mod.api('GET', '/orders')).rejects.toMatchObject({ status: 401 });
  expect(mod.getSession()).toBeNull();
  expect(localStorage.getItem('session')).toBeNull();
  expect(seen).toHaveBeenCalled();
});

test('logout clears the session and revokes the refresh token', async () => {
  const mod = await loggedIn();
  handler = () => ok(null);
  await mod.logout();
  expect(mod.getSession()).toBeNull();
  expect(calls[0]).toMatchObject({ method: 'POST', path: '/auth/logout', body: { refreshToken: 'r1' } });
});
