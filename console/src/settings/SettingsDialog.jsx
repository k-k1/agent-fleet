import { useState } from "react";
import { useApp } from "../state.jsx";
import ConnectionsTab from "./ConnectionsTab.jsx";
import AdminTab from "./AdminTab.jsx";
import DisplayTab from "./DisplayTab.jsx";
import ClaudeTab from "./ClaudeTab.jsx";

// SettingsDialog is the single modal in the app. Sections are chosen with a
// segmented control (matching the New Session modal), not tabs: Connections and
// Display for everyone, Admin for super_admin only.
export default function SettingsDialog() {
  const { closeSettings, superAdmin } = useApp();
  const [section, setSection] = useState("connections");

  const sections = [
    ["connections", "接続"],
    ["claude", "Claude"],
    ["display", "表示"],
    ...(superAdmin ? [["admin", "管理"]] : []),
  ];

  return (
    <div className="modal-backdrop" onClick={closeSettings}>
      <div className="modal settings-modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-head">
          <h3 className="modal-title">設定</h3>
          <button className="icon" title="閉じる" onClick={closeSettings}>
            ✕
          </button>
        </header>
        <div className="modal-body">
          <div className="seg settings-seg">
            {sections.map(([key, label]) => (
              <button
                key={key}
                type="button"
                className={"seg-btn" + (section === key ? " active" : "")}
                onClick={() => setSection(key)}
              >
                {label}
              </button>
            ))}
          </div>
          <div className="settings-content">
            {section === "connections" && <ConnectionsTab />}
            {section === "claude" && <ClaudeTab />}
            {section === "display" && <DisplayTab />}
            {section === "admin" && superAdmin && <AdminTab />}
          </div>
        </div>
      </div>
    </div>
  );
}
