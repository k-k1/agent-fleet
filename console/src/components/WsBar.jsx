import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { previewURL, ocwebURL } from "../api.js";
import Icon from "./Icon.jsx";
import Sparkline from "./Sparkline.jsx";

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
  const { wsState, startWs, stopWs, refreshWs, ocweb, wsStats, hostStats, wsHist, hostHist } = useApp();
  const isMobile = useIsMobile();
  const [port, setPort] = useState("");
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  const [moreOpen, setMoreOpen] = useState(false); // mobile overflow popover
  const pvRef = useRef(null);
  const moreRef = useRef(null);
  const running = wsState === "running";

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
      <span className="ws-state">{wsState}</span>
      <button onClick={startWs} disabled={running}>
        Start
      </button>
      <button onClick={stopWs} disabled={!running}>
        Stop
      </button>
      <button className="ghost" title="状態を更新" onClick={refreshWs}>
        <Icon name="refresh" />
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
              {graphs && <div className="ws-more-stats">{graphs}</div>}
              {ocwebBtn}
              {previewForm}
            </div>
          )}
        </div>
      ) : (
        <>
          {graphs}
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
