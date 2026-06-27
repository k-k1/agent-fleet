import { useState } from "react";
import { useApp } from "../state.jsx";
import ConnectionsTab from "./ConnectionsTab.jsx";
import AdminTab from "./AdminTab.jsx";

// SettingsDialog is the single modal in the app: Connections (everyone) and Admin
// (super_admin only). Opened from the top bar gear; closed via ✕ or backdrop.
export default function SettingsDialog() {
  const { closeSettings, superAdmin } = useApp();
  const [tab, setTab] = useState("connections");

  return (
    <div className="modal-backdrop" onClick={closeSettings}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-head">
          <div className="tabs">
            <button
              className={"tab" + (tab === "connections" ? " active" : "")}
              onClick={() => setTab("connections")}
            >
              接続
            </button>
            {superAdmin && (
              <button className={"tab" + (tab === "admin" ? " active" : "")} onClick={() => setTab("admin")}>
                管理
              </button>
            )}
          </div>
          <button className="icon" title="閉じる" onClick={closeSettings}>
            ✕
          </button>
        </header>
        <div className="modal-body">
          {tab === "connections" && <ConnectionsTab />}
          {tab === "admin" && superAdmin && <AdminTab />}
        </div>
      </div>
    </div>
  );
}
