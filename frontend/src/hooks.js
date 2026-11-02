import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { routeFromHash } from "./utils.js";

export function useRoute() {
  const [route, setRoute] = useState(() => routeFromHash(location.hash));
  useEffect(() => {
    const change = () => setRoute(routeFromHash(location.hash));
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  return route;
}

export function useResource(path, { interval = 0 } = {}) {
  const [state, setState] = useState({
    data: null,
    error: null,
    loading: !!path,
    updated: null,
  });
  const controller = useRef(null);
  const generation = useRef(0);
  const mounted = useRef(false);
  const reload = useCallback(
    async (quiet = false) => {
      if (!path) return;
      controller.current?.abort();
      const current = ++generation.current;
      controller.current = new AbortController();
      if (!quiet)
        setState((previous) => ({ ...previous, loading: true, error: null }));
      try {
        const data = await api(path, { signal: controller.current.signal });
        if (mounted.current && generation.current === current)
          setState({ data, error: null, loading: false, updated: new Date() });
        return data;
      } catch (error) {
        if (
          error.name !== "AbortError" &&
          mounted.current &&
          generation.current === current
        )
          setState((previous) => ({ ...previous, error, loading: false }));
      }
    },
    [path],
  );
  useEffect(() => {
    mounted.current = true;
    setState({ data: null, error: null, loading: !!path, updated: null });
    reload();
    const timer = interval
      ? setInterval(() => {
          if (document.visibilityState === "visible") reload(true);
        }, interval)
      : null;
    return () => {
      mounted.current = false;
      ++generation.current;
      controller.current?.abort();
      if (timer) clearInterval(timer);
    };
  }, [reload, interval, path]);
  return { ...state, reload };
}
