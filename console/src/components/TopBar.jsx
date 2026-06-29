import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { rel } from "../api.js";
import Icon from "./Icon.jsx";

// Top bar: product name, tenant picker (hidden for single-membership users), and
// an account menu folding in settings, admin (super_admin only) and sign-out
// (oauth mode). The menu shows whenever an identity resolved; otherwise a bare
// settings button keeps settings reachable.
export default function TopBar() {
  const { whoami, tenants, tenant, showPicker, selectTenant, openSettings, openAdmin, superAdmin, toggleNav } = useApp();
  const me = whoami?.email || whoami?.user || "";
  const canLogout = whoami?.auth_mode === "oauth"; // CP-native session we can clear
  const [menuOpen, setMenuOpen] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const acctRef = useRef(null);

  // Global fullscreen toggle (whole app). Tracks the browser's fullscreen state so
  // the icon/label flips even when fullscreen is exited via Esc.
  useEffect(() => {
    const onFs = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
  }, []);
  const toggleFullscreen = () => {
    if (document.fullscreenElement) document.exitFullscreen?.();
    else document.documentElement.requestFullscreen?.().catch(() => {});
  };

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

  const run = (fn) => {
    setMenuOpen(false);
    fn();
  };

  return (
    <header className="topbar">
      <button className="nav-toggle" title="メニュー（Sessions / Repos / Files）" onClick={toggleNav}>
        <Icon name="menu" />
      </button>
      <div className="brand">
        Agent Fleet <span className="brand-sub">Console</span>
      </div>
      <div className="topbar-right">
        <button
          className="gear fs-toggle"
          title={fullscreen ? "全画面解除" : "全画面表示"}
          onClick={toggleFullscreen}
        >
          <Icon name={fullscreen ? "screen-normal" : "screen-full"} />
        </button>
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
        {me ? (
          <div className="acct" ref={acctRef}>
            <button className="whoami acct-btn" title={me} onClick={() => setMenuOpen((o) => !o)}>
              <Icon name="account" /> <span className="acct-name">{me}</span>
              <Icon name="chevron-down" className="acct-caret" />
            </button>
            {menuOpen && (
              <div className="acct-menu" role="menu">
                <div className="acct-email" title={me}>{me}</div>
                <button className="acct-item" role="menuitem" onClick={() => run(openSettings)}>
                  <Icon name="gear" /> 設定
                </button>
                {superAdmin && (
                  <button className="acct-item" role="menuitem" onClick={() => run(openAdmin)}>
                    <Icon name="shield" /> 管理
                  </button>
                )}
                {canLogout && (
                  <>
                    <div className="acct-sep" />
                    <a className="acct-item" role="menuitem" href={rel("oauth2/logout")}>
                      <Icon name="sign-out" /> ログアウト
                    </a>
                  </>
                )}
              </div>
            )}
          </div>
        ) : (
          <button className="gear" title="設定（接続 / Claude / 環境 / 表示）" onClick={openSettings}>
            <Icon name="gear" /> 設定
          </button>
        )}
      </div>
    </header>
  );
}
