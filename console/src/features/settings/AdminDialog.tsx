// AdminDialog — ported from the old settings/AdminDialog.tsx (docs/22 P7c).
// The super_admin-only management surface (tenants / members / quotas / live
// resources). It is a near-full-screen overlay rather than a small centered
// modal: the staged drill-down (tenants → tenant → member) plus the per-member
// resource + session views need room. Kept separate from the per-user
// SettingsDialog so administration is clearly distinct from personal settings.
import { useSettingsUI } from "./store.ts";
import { AdminTab } from "./AdminTab.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useT } from "../../lib/i18n/index.ts";

export function AdminDialog() {
  const tr = useT();
  const closeAdmin = useSettingsUI((s) => s.closeAdmin);
  // Escape closes fully (all drill levels), same as the × / backdrop — matches the
  // ui/Modal behavior the settings dialog gets for free. Layered: with a confirm
  // (停止 / 掃除 / 権限付与) open on top, Esc cancels that first.
  useEscLayer(closeAdmin);
  return (
    <div className="ui-modal-backdrop" onClick={closeAdmin}>
      <div className="admin-surface" onClick={(e) => e.stopPropagation()}>
        <header className="admin-surface-head">
          <h3 className="modal-title">
            <Icon name="shield" /> {tr("admin.title")}
          </h3>
          <button type="button" className="icon" title={tr("common.close")} onClick={closeAdmin}>
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
