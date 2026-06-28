import { useApp } from "../state.jsx";
import Icon from "./Icon.jsx";

// Top bar: product name, tenant picker (hidden for single-membership users),
// the signed-in identity, and the settings button (Connections + Admin tabs).
export default function TopBar() {
  const { whoami, tenants, tenant, showPicker, selectTenant, openSettings, openAdmin, superAdmin, toggleNav } = useApp();
  const me = whoami?.email || whoami?.user || "";
  return (
    <header className="topbar">
      <button className="nav-toggle" title="メニュー（Sessions / Repos / Files）" onClick={toggleNav}>
        <Icon name="menu" />
      </button>
      <div className="brand">
        Agent Fleet <span className="brand-sub">Console</span>
      </div>
      <div className="topbar-right">
        {showPicker && (
          <label className="tenant-pick">
            <span className="lbl">Tenant</span>
            <select value={tenant} onChange={(e) => selectTenant(e.target.value)}>
              {tenants.map((t) => (
                <option key={t.slug} value={t.slug}>
                  {t.name} ({t.role})
                </option>
              ))}
            </select>
          </label>
        )}
        {me && <span className="whoami" title={me}>{me}</span>}
        {superAdmin && (
          <button className="gear" title="管理（テナント / メンバー / クォータ）" onClick={openAdmin}>
            <Icon name="shield" /> 管理
          </button>
        )}
        <button className="gear" title="設定（接続 / Claude / 環境 / 表示）" onClick={openSettings}>
          <Icon name="gear" /> 設定
        </button>
      </div>
    </header>
  );
}
