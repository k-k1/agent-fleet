import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { previewURL } from "../api.js";
import Icon from "./Icon.jsx";

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "作り直す" lives deep in 設定 > 環境 (warning-gated), off this bar.
// Port preview is a popover (a single button on the bar) so it never wraps to a
// second line on a phone.
export default function WsBar() {
  const { wsState, startWs, stopWs, refreshWs } = useApp();
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
