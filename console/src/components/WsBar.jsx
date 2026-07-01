import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { api, previewURL, ocwebURL } from "../api.js";
import Icon from "./Icon.jsx";
import Sparkline from "./Sparkline.jsx";

const HIST_N = 60; // sparkline ring buffer: ~4 min at the 4s poll cadence

// useWsResourceChips polls the workspace + host resource stats every 4s. It lives
// here (not in the global state provider) on purpose: these values change every
// tick, so holding them in the top-level context would re-render the whole app
// (terminals included) every 4s and jank/flicker the cursor. Confined to WsBar,
// only the small bar re-renders. Container stats are for everyone; host stats are
// super_admin-only (the CP gates /api/admin/host server-side too).
function useWsResourceChips(tenant, superAdmin) {
  const [wsStats, setWsStats] = useState(null);
  const [wsHist, setWsHist] = useState([]); // [{cpu, mem}]
  const wsKey = useRef("");
  useEffect(() => {
    let alive = true;
    wsKey.current = "";
    setWsHist([]); // tenant switch → start the trend fresh
    const load = () => {
      if (document.hidden) return; // a backgrounded tab needn't poll (or repaint)
      api("api/workspace/stats")
        .then((d) => {
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
          const key = ok ? `${!!(d && d.running)}|${memPct}|${cpuPct}` : "off";
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
        })
        .catch(() => {
          if (!alive || wsKey.current === "off") return;
          wsKey.current = "off";
          setWsStats(null);
          setWsHist([]);
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
  }, [tenant]);

  const [hostStats, setHostStats] = useState(null);
  const [hostHist, setHostHist] = useState([]); // [{load, mem}], load normalized to cores
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

// useClaudeUsage polls the Claude subscription usage (5-hour + weekly) every 60s —
// it changes slowly, and the endpoint (proxied to the Agent, which caches it) is
// rate-limited, so we poll far less often than the resource chips. Returns null
// whenever it's unavailable (workspace stopped, no token, endpoint changed), which
// simply hides the chip. Kept in WsBar like the resource poll so it doesn't re-render
// the whole app.
function useClaudeUsage(tenant) {
  const [usage, setUsage] = useState(null);
  useEffect(() => {
    let alive = true;
    const load = () => {
      if (document.hidden) return;
      api("api/claude/usage")
        .then((d) => alive && setUsage(d && d.ok ? d : null))
        .catch(() => alive && setUsage(null));
    };
    load();
    const id = setInterval(load, 60000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [tenant]);
  return usage;
}

// untilText: a reset instant → a compact relative "あとN日/N時間/N分" (the weekday+time
// the app shows is hard to read; relative is glanceable, the absolute date-time goes
// in the tooltip). whenText: the absolute local "M/D HH:MM".
function untilText(iso) {
  const t = new Date(iso).getTime();
  if (isNaN(t)) return "";
  const min = Math.max(0, Math.round((t - Date.now()) / 60000));
  if (min >= 1440) return `あと${Math.round(min / 1440)}日`;
  if (min >= 60) return `あと${Math.round(min / 60)}時間`;
  return `あと${min}分`;
}
function whenText(iso) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "作り直す" lives deep in 設定 > 環境 (warning-gated), off this bar.
// On desktop the resource chips / opencode web / port-preview sit inline at the
// right; on a phone they'd wrap to a second line, so they fold into a single ⋯
// overflow popover instead.
// GiB with adaptive precision: 2 decimals under 10 (so 0.98 stays visible),
// 1 above (so 26.9 stays compact).
const fg = (b) => {
  const v = b / 1073741824;
  return v < 10 ? v.toFixed(2) : v.toFixed(1);
};

// Friendly label for the raw container state. The CP returns docker-derived states
// (runtime.go state()): "running" | "stopped" | "none". Stop does `docker rm -f`,
// so the *normal* stopped state is "none" (no container — data persists in the bind
// mount, recreated on Start); "stopped" only appears when a container exists but
// exited on its own (crash / OOM). Both read as 停止 to the user; the raw state stays
// in the tooltip. Transient states (set optimistically in state.jsx) end in "…".
function wsLabel(s) {
  switch (s) {
    case "running":
      return "稼働中";
    case "none":
    case "stopped":
      return "停止";
    case "starting…":
      return "起動中…";
    case "recreating…":
      return "再作成中…";
    case "unknown":
      return "不明";
    default:
      return s; // "…" initial, or any future state
  }
}

// Matches the 760px breakpoint in styles.css (the phone layout).
function useIsMobile() {
  const [m, setM] = useState(() => typeof window !== "undefined" && window.matchMedia("(max-width: 760px)").matches);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 760px)");
    const fn = () => setM(mq.matches);
    mq.addEventListener("change", fn);
    return () => mq.removeEventListener("change", fn);
  }, []);
  return m;
}

// One resource tile: label + trend sparkline + current value, tinted by level
// (0 normal / 1 warn / 2 crit). Called as a plain function (not <Tile/>) so it's
// just inline JSX, no extra component instance.
function tile({ k, series, max, track, value, level, title }) {
  return (
    <span className={"ws-graph" + (level === 2 ? " crit" : level === 1 ? " warn" : "")} title={title} key={k + title}>
      <span className="ws-graph-k">{k}</span>
      <Sparkline data={series} max={max} track={track} />
      <span className="ws-graph-v">{value}</span>
    </span>
  );
}

export default function WsBar() {
  const { wsState, startWs, stopWs, ocweb, tenant, superAdmin, layout, resetToTerminal } = useApp();
  const { wsStats, wsHist, hostStats, hostHist } = useWsResourceChips(tenant, superAdmin);
  const usage = useClaudeUsage(tenant);
  const isMobile = useIsMobile();
  const [port, setPort] = useState("");
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  const [moreOpen, setMoreOpen] = useState(false); // mobile overflow popover
  const pvRef = useRef(null);
  const moreRef = useRef(null);
  const running = wsState === "running";
  const busy = wsState.endsWith("…"); // starting… / stopping… / recreating… — toggle inert

  // "Close all panes" collapses the split layout back to one empty terminal pane.
  // Disabled when there's already just a single empty pane (nothing to close).
  const totalPanes = layout.cols.reduce((n, c) => n + c.panes.length, 0);
  const onlyPane = totalPanes === 1 ? layout.cols[0].panes[0] : null;
  const canCloseAll = !(onlyPane && onlyPane.kind === "terminal" && !onlyPane.session && !onlyPane.filePath && !onlyPane.scmRepo);

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (!p) return;
    window.open(previewURL(p), "_blank", "noopener");
    setPvOpen(false);
    setMoreOpen(false);
  };

  // Close the popovers on an outside click / Escape.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    const onDown = (e) => {
      if (pvRef.current && !pvRef.current.contains(e.target)) setPvOpen(false);
      if (moreRef.current && !moreRef.current.contains(e.target)) setMoreOpen(false);
    };
    const onKey = (e) => {
      if (e.key === "Escape") {
        setPvOpen(false);
        setMoreOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [pvOpen, moreOpen]);

  // --- pieces shared between the inline (desktop) and folded (mobile) layouts ---
  const lvl = (v, warn, crit) => (v == null ? 0 : v >= crit ? 2 : v >= warn ? 1 : 0);

  // Container (own workspace): memory fill (vs quota) + CPU%. Shown to everyone.
  const hasWs = wsStats && wsStats.running && wsStats.mem_used != null;
  const memRatio = hasWs && wsStats.mem_max ? wsStats.mem_used / wsStats.mem_max : null;
  const containerTiles = hasWs && (
    <>
      {tile({
        k: "mem",
        series: wsHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: memRatio != null ? `${Math.round(memRatio * 100)}%` : `${fg(wsStats.mem_used)}G`,
        level: lvl(memRatio, 0.75, 0.9),
        title: `ワークスペースのメモリ: ${fg(wsStats.mem_used)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}G`,
      })}
      {tile({
        k: "cpu",
        series: wsHist.map((p) => p.cpu),
        value: wsStats.cpu_pct != null ? `${Math.round(wsStats.cpu_pct)}%` : "–",
        level: lvl(wsStats.cpu_pct, 60, 90),
        title: "ワークスペースの CPU 使用率（1コア = 100%）",
      })}
    </>
  );

  // Host (super_admin only): load average (normalized to cores) + total memory fill.
  const hasHost = hostStats && hostStats.mem_total != null;
  const loadNorm = hasHost && hostStats.ncpu ? hostStats.load1 / hostStats.ncpu : null;
  const hostMemRatio = hasHost ? hostStats.mem_used / hostStats.mem_total : null;
  const hostTiles = hasHost && (
    <>
      {tile({
        k: "ld",
        series: hostHist.map((p) => p.load),
        max: 1,
        track: true,
        value: Number(hostStats.load1).toFixed(2),
        level: lvl(loadNorm, 0.7, 1),
        title: `ホスト ロードアベレージ(1分): ${Number(hostStats.load1).toFixed(2)} / ${hostStats.ncpu}コア（管理者のみ）`,
      })}
      {tile({
        k: "mem",
        series: hostHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: hostMemRatio != null ? `${Math.round(hostMemRatio * 100)}%` : "",
        level: lvl(hostMemRatio, 0.8, 0.92),
        title: `ホスト メモリ: ${fg(hostStats.mem_used)}/${fg(hostStats.mem_total)}G（管理者のみ）`,
      })}
    </>
  );

  const graphs = (containerTiles || hostTiles) && (
    <>
      {containerTiles}
      {containerTiles && hostTiles && <span className="ws-graph-sep" />}
      {hostTiles}
    </>
  );

  // Claude subscription usage: 5-hour + weekly percent, each with a relative reset on
  // the chip and the absolute date-time in the tooltip. Colored by the higher of the
  // two. Unofficial/best-effort — absent (null) whenever the endpoint didn't answer,
  // so the chip just doesn't appear.
  const usageWin = (w) => ({ pct: Math.round(w.pct), until: untilText(w.resetsAt), when: whenText(w.resetsAt) });
  const uh = usage && usage.fiveHour && usageWin(usage.fiveHour);
  const uw = usage && usage.sevenDay && usageWin(usage.sevenDay);
  const usageChip = (uh || uw) && (() => {
    const face = [];
    const detail = ["Claude 使用状況"];
    if (uh) {
      face.push(`5h ${uh.pct}% ${uh.until}`);
      detail.push(`5時間制限 ${uh.pct}%・${uh.until}でリセット（${uh.when}）`);
    }
    if (uw) {
      face.push(`週 ${uw.pct}% ${uw.until}`);
      detail.push(`週次・全モデル ${uw.pct}%・${uw.until}でリセット（${uw.when}）`);
    }
    const maxPct = Math.max(uh ? uh.pct : 0, uw ? uw.pct : 0);
    const level = lvl(maxPct, 80, 95);
    return (
      <span
        className={"ws-graph ws-usage" + (level === 2 ? " crit" : level === 1 ? " warn" : "")}
        title={detail.join("\n")}
        key="claude-usage"
      >
        <span className="ws-graph-k">Claude</span>
        <span className="ws-graph-v">{face.join(" · ")}</span>
      </span>
    );
  })();

  const statsBlock = (graphs || usageChip) && (
    <>
      {graphs}
      {graphs && usageChip && <span className="ws-graph-sep" />}
      {usageChip}
    </>
  );

  const ocwebBtn = ocweb && ocweb.available && ocweb.enabled && (
    <button
      className="ghost"
      disabled={!running || !ocweb.running}
      title={ocweb.running ? "opencode web を新しいタブで開く" : "opencode web 起動中…"}
      onClick={() => {
        if (!ocweb.running) return;
        window.open(ocwebURL(), "_blank", "noopener");
        setMoreOpen(false);
      }}
    >
      <Icon name="globe" /> opencode web ↗
    </button>
  );

  const previewForm = (
    <>
      <label className="pv-label">ポートプレビュー</label>
      <div className="pv-row">
        <input
          className="preview-port"
          type="number"
          min="1"
          max="65535"
          placeholder="port"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && openPreview()}
          title="コンテナ内で起動したサービスのポート（例: 8080）"
        />
        <button onClick={openPreview} disabled={!port.trim()}>
          開く
        </button>
      </div>
      <div className="pv-hint">コンテナ内で起動したサービスのポート（例: 8080）を新しいタブで開きます。</div>
    </>
  );

  return (
    <div className="wsbar">
      <span className="ws-label">Workspace</span>
      <span className={"ws-dot " + (running ? "on" : "off")}>●</span>
      <span
        className="ws-state"
        title={
          wsState === "none"
            ? "停止（コンテナなし — Stop で削除済み。データは保持、Start で再作成）"
            : wsState === "stopped"
              ? "停止（コンテナが自走終了 — クラッシュ / OOM の可能性）"
              : `状態: ${wsState}`
        }
      >
        {wsLabel(wsState)}
      </span>
      {/* One toggle instead of separate Start/Stop: label + action follow the state.
          The bar auto-syncs wsState from the 4s stats poll (state.jsx), so an
          externally-changed workspace (admin stop / OOM) reflects without a manual
          refresh. Disabled mid-transition (starting…/stopping…). */}
      <button
        className={"ws-toggle " + (running ? "stop" : "start")}
        onClick={running ? stopWs : startWs}
        disabled={busy}
        title={running ? "ワークスペースを停止" : "ワークスペースを起動"}
      >
        {running ? "Stop" : "Start"}
      </button>
      <button
        className="ghost"
        title="全ペインを閉じる"
        disabled={!canCloseAll}
        onClick={resetToTerminal}
      >
        <Icon name="close-all" />
      </button>

      <span className="ws-spacer" />

      {isMobile ? (
        <div className="ws-more" ref={moreRef}>
          <button
            className="ghost ws-more-btn"
            title="リソース情報 / opencode web / プレビュー"
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </button>
          {moreOpen && (
            <div className="ws-more-pop">
              {statsBlock && <div className="ws-more-stats">{statsBlock}</div>}
              {ocwebBtn}
              {previewForm}
            </div>
          )}
        </div>
      ) : (
        <>
          {statsBlock}
          {ocwebBtn}
          <div className="ws-preview" ref={pvRef}>
            <button
              className="ghost ws-preview-btn"
              disabled={!running}
              title="コンテナ内サービスをポート指定で開く"
              onClick={() => setPvOpen((o) => !o)}
            >
              <Icon name="globe" /> プレビュー
            </button>
            {pvOpen && <div className="ws-preview-pop">{previewForm}</div>}
          </div>
        </>
      )}
    </div>
  );
}
