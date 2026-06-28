import { useApp } from "../state.jsx";
import AdminTab from "./AdminTab.jsx";
import Icon from "../components/Icon.jsx";

// AdminDialog is the super_admin-only management modal (tenants / members / quotas),
// kept separate from the per-user SettingsDialog so administration is clearly
// distinct from personal settings. Opened from its own top-bar button.
export default function AdminDialog() {
  const { closeAdmin } = useApp();
  return (
    <div className="modal-backdrop" onClick={closeAdmin}>
      <div className="modal settings-modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-head">
          <h3 className="modal-title">管理</h3>
          <button className="icon" title="閉じる" onClick={closeAdmin}>
            <Icon name="close" />
          </button>
        </header>
        <div className="modal-body">
          <div className="settings-content">
            <AdminTab />
          </div>
        </div>
      </div>
    </div>
  );
}
