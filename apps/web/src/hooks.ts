import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { api, getSession, subscribe } from './api';

export function useSession() {
  return useSyncExternalStore(subscribe, getSession);
}

// GETs `path` (skipped when null) and re-fetches when it changes or reload() is called.
export function useApi<T>(path: string | null) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!path) return;
    let live = true;
    api<T>('GET', path).then(
      (d) => live && (setData(d), setError(undefined)),
      (e: Error) => live && setError(e.message),
    );
    return () => {
      live = false;
    };
  }, [path, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, reload };
}
