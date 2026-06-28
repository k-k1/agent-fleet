import { useState } from "react";
import { useApp } from "../state.jsx";
import { previewURL } from "../api.js";
import ConfirmDialog from "./ConfirmDialog.jsx";
import Icon from "./Icon.jsx";

// WS bar: the (single) workspace's state plus Start / Stop / Recreate. The backend
// models one workspace per membership, so there is no select / create / delete —
// "作り直す" recreates the container from the current image (warning-gated).
export default function WsBar() {
  const { wsState, startWs, stopWs, recreateWs, refreshWs } = useApp();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [port, setPort] = useState("");
  const running = wsState === "running";

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (p) window.open(previewURL(p), "_blank", "noopener");
  };

  const doRecreate = async () => {
    setBusy(true);
    try {
      await recreateWs();
      setConfirm(false);
    } finally {
      setBusy(false);
    }
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
      <button className="danger-btn" title="コンテナを作り直す（最新イメージで再生成）" onClick={() => setConfirm(true)}>
        作り直す
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

      {confirm && (
        <ConfirmDialog
          title="Workspace を作り直しますか？"
          confirmLabel="作り直す"
          busy={busy}
          onConfirm={doRecreate}
          onCancel={() => setConfirm(false)}
        >
          <p>コンテナを破棄し、最新イメージで新しく作り直します。</p>
          <ul className="confirm-list">
            <li className="keep"><Icon name="check" /> ログイン・接続（GitHub / Bitbucket / Claude）は保持されます</li>
            <li className="lose"><Icon name="close" /> 実行中のセッションは失われます</li>
            <li className="lose"><Icon name="close" /> clone 済みリポジトリ（未コミット変更を含む）は削除されます</li>
          </ul>
        </ConfirmDialog>
      )}
    </div>
  );
}
