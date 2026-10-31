// Every call goes through the gateway at /api/v1, which answers with the {data, error, meta} envelope.
export type User = { id: string; email: string; role: string };
type Session = { accessToken: string; refreshToken: string; user: User };
type Tokens = { accessToken: string; refreshToken: string };

const KEY = 'session';

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

let current: Session | null | undefined; // undefined until first read from localStorage
const listeners = new Set<() => void>();

function stored(): Session | null {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? 'null');
  } catch {
    return null;
  }
}

export function getSession(): Session | null {
  if (current === undefined) current = stored();
  return current;
}

// Another tab logged in, out, or rotated the tokens: drop the cache so the next read sees it.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key !== KEY && e.key !== null) return;
    current = undefined;
    listeners.forEach((f) => f());
  });
}

// The session another tab may have stored since `s` was read, if it differs.
function newerThan(s: Session): Session | null {
  const fresh = stored();
  if (!fresh || fresh.accessToken === s.accessToken) return null;
  current = fresh;
  listeners.forEach((f) => f());
  return fresh;
}

function setSession(s: Session | null) {
  current = s;
  if (s) localStorage.setItem(KEY, JSON.stringify(s));
  else localStorage.removeItem(KEY);
  listeners.forEach((f) => f());
}

export function subscribe(f: () => void) {
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

async function send(method: string, path: string, body: unknown, token?: string) {
  const headers: Record<string, string> = {};
  if (token) headers.authorization = `Bearer ${token}`;
  if (body !== undefined) headers['content-type'] = 'application/json';
  const res = await fetch('/api/v1' + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, json.error?.code ?? 'HTTP_ERROR', json.error?.message ?? res.statusText);
  return json.data;
}

// Refresh tokens rotate (the old one is revoked), so parallel 401s must share one refresh.
let refreshing: Promise<Session | null> | null = null;

function refresh(s: Session): Promise<Session | null> {
  refreshing ??= send('POST', '/auth/refresh', { refreshToken: s.refreshToken })
    .then(
      (t: Tokens) => {
        const next = { ...s, ...t };
        setSession(next);
        return next;
      },
      // Lost a race with another tab that rotated the same refresh token: use its session, else log out.
      () => newerThan(s) ?? (setSession(null), null),
    )
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const s = getSession();
  try {
    return await send(method, path, body, s?.accessToken);
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 401 && s)) throw err;
    const next = newerThan(s) ?? (await refresh(s));
    if (!next) throw err;
    return send(method, path, body, next.accessToken);
  }
}

export async function login(email: string, password: string) {
  const t: Tokens = await send('POST', '/auth/login', { email, password });
  const user: User = await send('GET', '/auth/me', undefined, t.accessToken);
  setSession({ ...t, user });
}

export async function signup(email: string, password: string, role: string) {
  await send('POST', '/auth/signup', { email, password, role });
  await login(email, password);
}

export async function logout() {
  const s = getSession();
  setSession(null);
  if (s) await send('POST', '/auth/logout', { refreshToken: s.refreshToken }, s.accessToken).catch(() => {});
}

// WebSockets cannot send headers, so the gateway takes the token from ?access_token=.
export function socket(path: string) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  return new WebSocket(`${proto}://${location.host}/api/v1/ws${path}?access_token=${getSession()?.accessToken ?? ''}`);
}
