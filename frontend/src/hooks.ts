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
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      const d = await fetcherRef.current();
      setData(d);
      setError(undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      inflight.current = false;
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!enabled) return;
    void refresh();
    let timer: number | undefined;
    const start = () => {
      stop();
      timer = window.setInterval(() => void refresh(), intervalMs);
    };
    const stop = () => window.clearInterval(timer);
    const onVisibility = () => {
      if (document.hidden) stop();
      else {
        void refresh();
        start();
      }
    };
    start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [enabled, intervalMs, refresh]);

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
