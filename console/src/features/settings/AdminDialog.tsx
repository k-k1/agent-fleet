// AdminDialog — super_admin の管理面（テナント / メンバー / 上限 / 稼働状況）。
//
// 器は個人設定・テナント設定と同じ ui/Modal（`settings-modal`）。以前は独自の
// 全画面サーフェス（.admin-surface）で、閉じる・Esc・端末の戻る・フォームの配色まで
// 自前だった。中身のナビをレール化したことで大きさ以外に違う理由が無くなったので、
// 器ごと共通の Modal に寄せた（幅・高さだけ .admin-modal で 1100×900 のまま——
// テナントのカードとメンバーの表は個人設定より横が要る）。
//
// 個人設定と分けてあるのは変わらない: 管理の操作が個人の設定に紛れないため。
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
