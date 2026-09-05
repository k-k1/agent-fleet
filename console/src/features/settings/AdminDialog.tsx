// AdminDialog — the super_admin surface (tenants / members / quotas / uptime).
//
// The shell is the same ui/Modal (`settings-modal`) as personal and tenant settings, so close,
// Esc, device back and form colours all behave identically; only the size differs (.admin-modal
// keeps 1100x900, because the tenant cards and the member table need more width than personal
// settings).
//
// It stays a separate modal from personal settings so admin actions never blend into a user's
// own preferences.
import { useSettingsUI } from "./store.ts";
import { AdminTab } from "./admin/AdminTab.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function AdminDialog() {
  const tr = useT();
  const closeAdmin = useSettingsUI((s) => s.closeAdmin);
  return (
    <Modal
      title={
        <>
          <Icon name="shield" /> {tr("admin.title")}
        </>
      }
      onClose={closeAdmin}
      className="settings-modal admin-modal"
    >
      <div className="ui-modal-body">
        <AdminTab />
      </div>
    </Modal>
  );
}
