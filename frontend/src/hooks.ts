import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

export interface Poll<T> {
  data: T | undefined;
  error: string | undefined;
  loading: boolean;
  refresh: () => Promise<void>;
}

/**
 * Calls fetcher now and every intervalMs while the tab is visible.
 * Pass enabled=false to stop polling (the last data is kept).
 */
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number, enabled = true): Poll<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const inflight = useRef(0);
  const latest = useRef(0);

  // Interval ticks skip while a fetch is running. An explicit refresh (after
  // an action) always fetches, and only the newest fetch's result is used,
  // so a poll that started before the action can't overwrite it with
  // stale data.
  const load = useCallback(async (force: boolean) => {
    if (!force && inflight.current > 0) return;
    const seq = ++latest.current;
    inflight.current++;
    try {
      const d = await fetcherRef.current();
      if (seq === latest.current) {
        setData(d);
        setError(undefined);
      }
    } catch (e) {
      if (seq === latest.current) setError(e instanceof Error ? e.message : String(e));
    } finally {
      inflight.current--;
      if (seq === latest.current) setLoading(false);
    }
  }, []);
  const refresh = useCallback(() => load(true), [load]);
  const tick = useCallback(() => load(false), [load]);

  useEffect(() => {
    if (!enabled) return;
    void tick();
    let timer: number | undefined;
    const start = () => {
      stop();
      timer = window.setInterval(() => void tick(), intervalMs);
    };
    const stop = () => window.clearInterval(timer);
    const onVisibility = () => {
      if (document.hidden) stop();
      else {
        void tick();
        start();
      }
    };
    start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [enabled, intervalMs, tick]);

  return { data, error, loading, refresh };
}

/** Width of an element, updated as it resizes. */
export function useWidth<E extends HTMLElement>(): [RefObject<E | null>, number] {
  const ref = useRef<E>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.floor(entry.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width];
}

/** The current hash (without '#'), kept in sync with navigation. */
export function useHash(fallback: string): [string, (h: string) => void] {
  const read = () => window.location.hash.slice(1) || fallback;
  const [hash, setHash] = useState(read);
  useEffect(() => {
    const on = () => setHash(read());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  const set = useCallback((h: string) => {
    window.location.hash = h;
  }, []);
  return [hash, set];
}
