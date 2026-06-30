import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { previewURL, ocwebURL } from "../api.js";
import Icon from "./Icon.jsx";

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "作り直す" lives deep in 設定 > 環境 (warning-gated), off this bar.
// Port preview is a popover (a single button on the bar) so it never wraps to a
// second line on a phone.
// GiB with adaptive precision: 2 decimals under 10 (so 0.98 stays visible),
// 1 above (so 26.9 stays compact).
const fg = (b) => {
  const v = b / 1073741824;
  return v < 10 ? v.toFixed(2) : v.toFixed(1);
};

export default function WsBar() {
  const { wsState, startWs, stopWs, refreshWs, ocweb, wsStats, hostStats } = useApp();
  const [port, setPort] = useState("");
  const [pvOpen, setPvOpen] = useState(false);
  const pvRef = useRef(null);
  const running = wsState === "running";

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (!p) return;
    window.open(previewURL(p), "_blank", "noopener");
    setPvOpen(false);
  };

  // Close the popover on an outside click / Escape.
  useEffect(() => {
    if (!pvOpen) return;
    const onDown = (e) => {
      if (pvRef.current && !pvRef.current.contains(e.target)) setPvOpen(false);
    };
    const onKey = (e) => e.key === "Escape" && setPvOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [pvOpen]);

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
      {wsStats && wsStats.running && wsStats.mem_used != null && (
        <span
          className={
            "ws-stat" +
            (wsStats.mem_max && wsStats.mem_used / wsStats.mem_max >= 0.9 ? " warn" : "")
          }
          title="このワークスペースのメモリ / CPU（コンテナのクォータに対する使用量）"
        >
          ws {fg(wsStats.mem_used)}
          {wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}G
          {wsStats.cpu_pct != null && ` · cpu ${Math.round(wsStats.cpu_pct)}%`}
        </span>
      )}
      {hostStats && hostStats.mem_total != null && (
        <span
          className={"ws-stat" + (hostStats.load1 > hostStats.ncpu ? " warn" : "")}
          title="ホスト: 1分ロードアベレージ / CPUコア数, 総メモリ使用量（管理者のみ）"
        >
          host ld {Number(hostStats.load1).toFixed(2)}/{hostStats.ncpu} · mem{" "}
          {fg(hostStats.mem_used)}/{fg(hostStats.mem_total)}G
        </span>
      )}
      {ocweb && ocweb.available && ocweb.enabled && (
        <button
          className="ghost"
          disabled={!running || !ocweb.running}
          title={ocweb.running ? "opencode web を新しいタブで開く" : "opencode web 起動中…"}
          onClick={() => ocweb.running && window.open(ocwebURL(), "_blank", "noopener")}
        >
          <Icon name="globe" /> opencode web ↗
        </button>
      )}
      <div className="ws-preview" ref={pvRef}>
        <button
          className="ghost ws-preview-btn"
          disabled={!running}
          title="コンテナ内サービスをポート指定で開く"
          onClick={() => setPvOpen((o) => !o)}
        >
          <Icon name="globe" /> プレビュー
        </button>
        {pvOpen && (
          <div className="ws-preview-pop">
            <label className="pv-label">ポートプレビュー</label>
            <div className="pv-row">
              <input
                className="preview-port"
                type="number"
                min="1"
                max="65535"
                placeholder="port"
                value={port}
                autoFocus
                onChange={(e) => setPort(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && openPreview()}
                title="コンテナ内で起動したサービスのポート（例: 8080）"
              />
              <button onClick={openPreview} disabled={!port.trim()}>
                開く
              </button>
            </div>
            <div className="pv-hint">コンテナ内で起動したサービスのポート（例: 8080）を新しいタブで開きます。</div>
          </div>
        )}
      </div>
    </div>
  );
}
