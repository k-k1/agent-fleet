import { useApp } from "../state.jsx";
import AdminTab from "./AdminTab.jsx";
import Modal from "../components/Modal.jsx";

// AdminDialog is the super_admin-only management modal (tenants / members / quotas),
// kept separate from the per-user SettingsDialog so administration is clearly
// distinct from personal settings. Opened from its own top-bar button.
export default function AdminDialog() {
  const { closeAdmin } = useApp();
  return (
    <Modal title="管理" onClose={closeAdmin} className="settings-modal">
      <div className="modal-body">
        <div className="settings-content">
          <AdminTab />
        </div>
      </div>
    </Modal>
  );
}
