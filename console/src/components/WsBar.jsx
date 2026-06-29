import { useState } from "react";
import { useApp } from "../state.jsx";
import { previewURL } from "../api.js";
import Icon from "./Icon.jsx";

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "作り直す" lives deep in 設定 > 環境 (warning-gated), off this bar.
export default function WsBar() {
  const { wsState, startWs, stopWs, refreshWs } = useApp();
  const [port, setPort] = useState("");
  const running = wsState === "running";

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (p) window.open(previewURL(p), "_blank", "noopener");
  };

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
      <input
        className="preview-port"
        type="number"
        min="1"
        max="65535"
        placeholder="port"
        value={port}
        disabled={!running}
        onChange={(e) => setPort(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && openPreview()}
        title="コンテナ内で起動したサービスのポート（例: 8080）"
      />
      <button onClick={openPreview} disabled={!running || !port.trim()} title="新しいタブでプレビュー">
        プレビュー
      </button>
    </div>
  );
}
