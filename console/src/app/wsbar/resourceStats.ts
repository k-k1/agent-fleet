const HIST_N = 60; // sparkline ring buffer: ~4 min at the 4s poll cadence

interface WsHistPoint {
  cpu: number | null;
  mem: number | null;
}
interface HostHistPoint {
  load: number | null;
  mem: number;
}

// Partial type covering only the fields the WS bar displays from the api/workspace/stats
// and api/admin/host responses; the Agent/CP side owns the full shape, and unknown fields
// pass through.
interface WsStats {
  running?: boolean;
  mem_used?: number | null;
  mem_max?: number | null;
  cpu_pct?: number | null;
  oom_recent?: boolean;
  oom_killed?: boolean;
  exit_code?: number | null;
}
interface HostStats {
  mem_total: number;
  mem_used: number;
  load1: number;
  ncpu?: number;
}

// useWsResourceChips polls the workspace + host resource stats every 4s. It lives
// here (not in a global store) on purpose: these values change every tick, so
// holding them in shared state would re-render the whole app (terminals included)
// every 4s and jank/flicker the cursor. Confined to WsBar, only the small bar
// re-renders. Container stats are for everyone; host stats are super_admin-only
// (the CP gates /api/admin/host server-side too).
function useWsResourceChips(tenant: string | null, superAdmin: boolean) {
  const [wsStats, setWsStats] = useState<WsStats | null>(null);
  const [wsHist, setWsHist] = useState<WsHistPoint[]>([]); // [{cpu, mem}]
  const wsKey = useRef("");
  useEffect(() => {
    let alive = true;
    wsKey.current = "";
    setWsHist([]); // tenant switch → start the trend fresh
    // apply adopts one stats payload — from the 4s poll or a pushed api/events
    // frame (identical shape).
    const apply = (d: any) => {
      if (!alive) return;
      const ok = d && !d.error;
      const running = !!(ok && d.running && d.mem_used != null);
      // Re-render only when a DISPLAYED value changes. memory.current jitters
      // by bytes every read, so keying on the raw stats would repaint the
      // sparkline every 4s even at a steady 0% — which on a remote display
      // flickered the cursor. Key on the rounded mem%/cpu% the chip actually
      // shows, so an idle workspace produces the same key and no re-render.
      const memPct = running && d.mem_max ? Math.round((d.mem_used / d.mem_max) * 100) : -1;
      const cpuPct = running && d.cpu_pct != null ? Math.round(d.cpu_pct) : -1;
      // OOM flags are part of the key so a fresh in-container OOM (oom_recent) or a
      // stopped-from-OOM container (oom_killed) repaints even at a steady mem/cpu.
      const oom = `${d.oom_recent ? 1 : 0}|${d.oom_killed ? 1 : 0}|${d.exit_code ?? ""}`;
      const key = ok ? `${!!(d && d.running)}|${memPct}|${cpuPct}|${oom}` : "off";
      if (key === wsKey.current) return;
      wsKey.current = key;
      setWsStats(ok ? d : null);
      if (running) {
        const mem = d.mem_max ? d.mem_used / d.mem_max : null;
        const cpu = typeof d.cpu_pct === "number" ? d.cpu_pct : null;
        setWsHist((h) => [...h, { cpu, mem }].slice(-HIST_N));
      } else {
        setWsHist([]); // stopped / unreachable → drop the stale trend
      }
    };
    const load = () => {
      // a backgrounded tab needn't poll (or repaint); while the push channel is
      // live the stats frames arrive on their own (the poll is the fallback)
      if (document.hidden || pushHealthy()) return;
      api("api/workspace/stats")
        .then(apply)
        .catch(() => {
          if (!alive || wsKey.current === "off") return;
          wsKey.current = "off";
          setWsStats(null);
          setWsHist([]);
        });
    };
    const unPush = onPush("stats", apply);
    load();
    const id = setInterval(load, 4000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      unPush();
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [tenant]);

  const [hostStats, setHostStats] = useState<HostStats | null>(null);
  const [hostHist, setHostHist] = useState<HostHistPoint[]>([]); // [{load, mem}], load normalized to cores
  const hostKey = useRef("");
  useEffect(() => {
    if (!superAdmin) {
      setHostStats(null);
      setHostHist([]);
      return;
    }
    let alive = true;
    hostKey.current = "";
    const load = () => {
      if (document.hidden) return;
      api("api/admin/host")
        .then((d) => {
          if (!alive) return;
          const ok = d && d.mem_total != null;
          // Key on the displayed load (2dp) + rounded mem% so a steady host
          // doesn't repaint the chips every 4s.
          const key = ok ? `${Number(d.load1).toFixed(2)}|${Math.round((d.mem_used / d.mem_total) * 100)}` : "off";
          if (key === hostKey.current) return;
          hostKey.current = key;
          setHostStats(ok ? d : null);
          if (ok) {
            const ldNorm = d.ncpu ? d.load1 / d.ncpu : null;
            const mem = d.mem_used / d.mem_total;
            setHostHist((h) => [...h, { load: ldNorm, mem }].slice(-HIST_N));
          }
        })
        .catch(() => {
          if (!alive || hostKey.current === "off") return;
          hostKey.current = "off";
          setHostStats(null);
        });
    };
    load();
    const id = setInterval(load, 4000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [superAdmin]);

  return { wsStats, wsHist, hostStats, hostHist };
}
