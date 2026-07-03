import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { rel } from "../api.js";
import { useSettings, setSetting, THEMES, SURFACE_TARGETS } from "../lib/settings.js";
import Icon from "./Icon.jsx";
import SwatchGrid from "./SwatchGrid.jsx";
import { useDismiss } from "../lib/useDismiss.js";

// Top bar: product name, tenant picker (hidden for single-membership users), and
// an account menu folding in settings, admin (super_admin only) and sign-out
// (oauth mode). The menu shows whenever an identity resolved; otherwise a bare
// settings button keeps settings reachable.
export default function TopBar() {
  const { whoami, tenants, tenant, showPicker, selectTenant, openSettings, openAdmin, superAdmin, toggleNav } = useApp();
  const s = useSettings();
  const me = whoami?.email || whoami?.user || "";
  const canLogout = whoami?.auth_mode === "oauth"; // CP-native session we can clear
  const [menuOpen, setMenuOpen] = useState(false);
  const [apprOpen, setApprOpen] = useState(false); // 外観 (theme + surface colors) popover
  const [fullscreen, setFullscreen] = useState(false);
  const acctRef = useRef<HTMLDivElement>(null);
  const apprRef = useRef<HTMLDivElement>(null);

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

  // Close the account menu / 外観 popover on an outside click or Escape. The 外観
  // popover is kept separate from the account menu so surface colors can be tuned in
  // a light popover while the panes stay visible behind it (a full settings modal
  // would hide the live preview).
  useDismiss(acctRef, menuOpen, () => setMenuOpen(false));
  useDismiss(apprRef, apprOpen, () => setApprOpen(false));

  const run = (fn: () => void) => {
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
        {/* 外観: a light popover so colors preview live on the panes behind it. */}
        <div className="acct appr" ref={apprRef}>
          <button
            className="gear appr-btn"
            title="外観（テーマ・配色）"
            onClick={() => setApprOpen((o) => !o)}
          >
            <Icon name="paintcan" />
          </button>
          {apprOpen && (
            <div className="acct-menu appr-menu" role="menu">
              <div className="acct-email">外観</div>
              <div className="acct-theme">
                <div className="seg choice-seg acct-theme-seg">
                  {THEMES.map((t) => (
                    <button
                      key={t.id}
                      type="button"
                      className={"seg-btn" + (s.theme === t.id ? " active" : "")}
                      onClick={() => setSetting("theme", t.id)}
                    >
                      {t.label}
                    </button>
                  ))}
                </div>
                {SURFACE_TARGETS.map((t) => (
                  <SwatchRow key={t.key} label={t.short} theme={s.theme} value={s[t.key]} onPick={(v) => setSetting(t.key, v)} />
                ))}
              </div>
            </div>
          )}
        </div>
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
                <button className="acct-item" role="menuitem" onClick={() => run(() => openSettings())}>
                  <Icon name="gear" /> 設定
                </button>
                {(superAdmin || tenants?.some((t) => t.role === "tenant_admin")) && (
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
          <button className="gear" title="設定（表示 / ワークスペース / エージェント / Git / AWS SSM / MCP）" onClick={() => openSettings()}>
            <Icon name="gear" /> 設定
          </button>
        )}
      </div>
    </header>
  );
}

// SwatchRow: a labeled row wrapping the shared SwatchGrid (surface-color picker) for
// the 外観 popover. Tapping applies immediately and keeps the popover open.
interface SwatchRowProps {
  label: string;
  theme: string;
  value: string;
  onPick: (v: string) => void;
}

function SwatchRow({ label, theme, value, onPick }: SwatchRowProps) {
  return (
    <div className="acct-swatch-row">
      <span className="acct-swatch-lbl">{label}</span>
      <SwatchGrid theme={theme} value={value} onChange={onPick} />
    </div>
  );
}
