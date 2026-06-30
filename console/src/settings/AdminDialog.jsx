import { useApp } from "../state.jsx";
import AdminTab from "./AdminTab.jsx";
import Icon from "../components/Icon.jsx";

// AdminDialog is the super_admin-only management surface (tenants / members /
// quotas / live resources). It is a near-full-screen overlay rather than a small
// centered modal: the staged drill-down (tenants → tenant → member) plus the
// per-member resource + session views need room. Kept separate from the per-user
// SettingsDialog so administration is clearly distinct from personal settings.
export default function AdminDialog() {
  const { closeAdmin } = useApp();
  return (
    <div className="modal-backdrop" onClick={closeAdmin}>
      <div className="admin-surface" onClick={(e) => e.stopPropagation()}>
        <header className="admin-surface-head">
          <h3 className="modal-title">
            <Icon name="shield" /> 管理
          </h3>
          <button type="button" className="icon" title="閉じる" onClick={closeAdmin}>
            <Icon name="close" />
          </button>
        </header>
        <div className="admin-surface-body">
          <AdminTab />
        </div>
      </div>
    </div>
  );
}
