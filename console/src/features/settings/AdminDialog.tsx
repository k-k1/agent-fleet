// AdminDialog — P7c で旧 settings/AdminDialog+AdminTab(1,378行) を移植予定の
// プレースホルダ。openAdmin の履歴エントリ/クローズ動線だけ先に本実装している。
import { Modal } from "../../ui/Modal.tsx";
import { useSettingsUI } from "./store.ts";

export function AdminDialog() {
  const closeAdmin = useSettingsUI((s) => s.closeAdmin);
  return (
    <Modal title="管理" onClose={closeAdmin} className="admin-modal">
      <div className="ui-modal-body">
        <div className="ui-field-hint" style={{ padding: "24px 8px" }}>
          管理ダイアログ（テナント / メンバー / クォータ / 稼働セッション / 監査ログ）は移植中です（P7c）。
          それまでは旧コンソール（index.html）の 管理 をご利用ください。
        </div>
      </div>
    </Modal>
  );
}
