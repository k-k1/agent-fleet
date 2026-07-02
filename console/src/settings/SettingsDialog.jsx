import { useState } from "react";
import { useApp } from "../state.jsx";
import ConnectionsTab from "./ConnectionsTab.jsx";
import DisplayTab from "./DisplayTab.jsx";
import AgentsTab from "./AgentsTab.jsx";
import EnvTab from "./EnvTab.jsx";
import TokensTab from "./TokensTab.jsx";
import SsmTab from "./SsmTab.jsx";
import Modal from "../components/Modal.jsx";

// SettingsDialog holds the per-user settings (Connections / エージェント / 環境 / 表示).
// Super_admin (tenant/member/quota) management lives in a SEPARATE modal — see
// AdminDialog, opened from its own top-bar button — so admin actions are clearly
// distinct from personal settings.
export default function SettingsDialog() {
  const { closeSettings } = useApp();
  const [section, setSection] = useState("connections");

  const sections = [
    ["connections", "接続"],
    ["agents", "エージェント"],
    ["env", "環境"],
    ["ssm", "SSM"],
    ["tokens", "MCP"],
    ["display", "表示"],
  ];

  return (
    <Modal title="設定" onClose={closeSettings} className="settings-modal">
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
          {section === "agents" && <AgentsTab />}
          {section === "env" && <EnvTab />}
          {section === "ssm" && <SsmTab />}
          {section === "tokens" && <TokensTab />}
          {section === "display" && <DisplayTab />}
        </div>
      </div>
    </Modal>
  );
}
