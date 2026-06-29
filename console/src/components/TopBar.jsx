import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { rel } from "../api.js";
import Icon from "./Icon.jsx";

// Top bar: product name, tenant picker (hidden for single-membership users),
// the signed-in identity (with a sign-out menu in oauth mode), and the settings
// button (Connections + Admin tabs).
export default function TopBar() {
  const { whoami, tenants, tenant, showPicker, selectTenant, openSettings, openAdmin, superAdmin, toggleNav } = useApp();
  const me = whoami?.email || whoami?.user || "";
  const canLogout = whoami?.auth_mode === "oauth"; // CP-native session we can clear
  const [menuOpen, setMenuOpen] = useState(false);
  const acctRef = useRef(null);

  // Close the account menu on an outside click or Escape.
  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e) => {
      if (acctRef.current && !acctRef.current.contains(e.target)) setMenuOpen(false);
    };
    const onKey = (e) => e.key === "Escape" && setMenuOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [menuOpen]);

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
        {me &&
          (canLogout ? (
            <div className="acct" ref={acctRef}>
              <button className="whoami acct-btn" title={me} onClick={() => setMenuOpen((o) => !o)}>
                <Icon name="account" /> <span className="acct-name">{me}</span>
              </button>
              {menuOpen && (
                <div className="acct-menu" role="menu">
                  <div className="acct-email" title={me}>{me}</div>
                  <a className="acct-item" role="menuitem" href={rel("oauth2/logout")}>
                    <Icon name="sign-out" /> ログアウト
                  </a>
                </div>
              )}
            </div>
          ) : (
            <span className="whoami" title={me}>{me}</span>
          ))}
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
