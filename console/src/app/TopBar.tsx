// Top bar — ported from the old components/TopBar.tsx (docs/22 P6b). Verbatim
// except the useApp() reads (→ core/store/tenant), the nav toggles (→ props from
// App) and openSettings/openAdmin (→ features/settings/store).
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useTtsStore } from "../core/store/tts.ts";
import { useSettingsUI } from "../features/settings/store.ts";
import { rel, clearLocalState } from "../core/api/client.ts";
import { useSettings, setSetting, THEMES, SURFACE_TARGETS } from "../lib/settings.ts";
import { useIsMobile, isStandalonePWA } from "../lib/device.ts";
import { Icon } from "../ui/Icon.tsx";
import { SwatchGrid } from "../ui/SwatchGrid.tsx";
import { useDismiss } from "../lib/useDismiss.ts";
import { NotificationCenter } from "../features/notifications/NotificationCenter.tsx";

interface TopBarProps {
  toggleNav: () => void;
  toggleLeft: () => void;
  toggleLeftMode: () => void;
}

// Top bar: product name, tenant picker (hidden for single-membership users), and
// an account menu folding in settings, admin (super_admin only) and sign-out
// (oauth mode). The menu shows whenever an identity resolved; otherwise a bare
// settings button keeps settings reachable.
export function TopBar({ toggleNav, toggleLeft, toggleLeftMode }: TopBarProps) {
  const whoami = useTenantStore((s) => s.whoami);
  const tenants = useTenantStore((s) => s.tenants);
  const tenant = useTenantStore((s) => s.tenant);
  const showPicker = useTenantStore((s) => s.showPicker);
  const selectTenant = useTenantStore((s) => s.select);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  const s = useSettings();
  const isMobile = useIsMobile();
  // 音声読み上げ（docs/24）: ピルは常時表示。再生中は「読み上げ中＋停止」（クリックで全体
  // 1 本の再生を止める）、アイドル時は設定 ttsEnabled の ON/OFF トグルとして働く。
  // ttsSessionNotify（音声通知）は別軸なので、OFF でも speaking になり得る＝speaking 優先。
  const ttsSpeaking = useTtsStore((st) => st.speaking);
  const ttsPreparing = useTtsStore((st) => st.preparing);
  const ttsSource = useTtsStore((st) => st.source);
  const ttsVoice = useTtsStore((st) => st.voice);
  const ttsPurpose = useTtsStore((st) => st.purpose);
  const ttsBusy = ttsSpeaking || ttsPreparing; // 生成中（最初の音の前）もピルは再生扱い
  // Hamburger: single-click toggles the left pane open/closed; double-click toggles
  // its desktop display mode (Push ⇄ overlay). We debounce the single action so a
  // double-click doesn't also fire it. Mobile keeps the immediate drawer toggle.
  const clickTimer = useRef<number | null>(null);
  const onHamburger = () => {
    if (isMobile) {
      toggleNav();
      return;
    }
    if (clickTimer.current != null) {
      clearTimeout(clickTimer.current);
      clickTimer.current = null;
      toggleLeftMode();
      return;
    }
    clickTimer.current = window.setTimeout(() => {
      clickTimer.current = null;
      toggleLeft();
    }, 230);
  };
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

  const openSettings = useSettingsUI((st) => st.openSettings);
  const openAdmin = useSettingsUI((st) => st.openAdmin);
  const openGuide = useSettingsUI((st) => st.openGuide);
  const run = (fn: () => void) => {
    setMenuOpen(false);
    fn();
  };

  return (
    <header className="topbar">
      <div className="topbar-left">
        <button
          className="nav-toggle"
          title="左パネル: クリックで開閉 / ダブルクリックで表示切替（Push⇄オーバーレイ）"
          onClick={onHamburger}
        >
          <Icon name="menu" />
        </button>
        <div className="brand">
          Agent Fleet <span className="brand-sub">Console</span>
        </div>
      </div>
      <div className="topbar-right">
        <button
          className={"tts-status" + (ttsBusy ? " speaking" : s.ttsEnabled ? "" : " off")}
          title={
            ttsBusy
              ? "読み上げを停止して OFF"
              : s.ttsEnabled
                ? "音声読み上げ: ON（クリックで OFF）"
                : "音声読み上げ: OFF（クリックで ON）"
          }
          onClick={() => {
            // 再生中のクリックは「今の1本を止める」だけでなく設定も OFF にする。以前は停止のみ
            // で ttsEnabled が ON のまま残り、次の新規セッションの回答でまた自動再生された
            // （＝「OFF にしたのに勝手に ON」の主因）。喋っている最中に押す＝黙らせたい意思、
            // として停止＋OFF をひとまとめにする。ON へ戻すのはアイドル時のクリック。
            if (ttsBusy) {
              useTtsStore.getState().stop();
              if (ttsPurpose === "session-notification") setSetting("ttsSessionNotify", false);
              else if (ttsPurpose === "usage-notification") setSetting("usageResetNotify", false);
              else if (ttsPurpose !== "manual") setSetting("ttsEnabled", false);
              return;
            }
            if (s.ttsEnabled) setSetting("ttsEnabled", false);
            else if (!ttsBusy) setSetting("ttsEnabled", true);
          }}
        >
          {/* 最初の音が鳴る前（合成待ち）はぐるぐるで「生成中」を示す */}
          <Icon
            name={ttsPreparing ? "loading" : ttsBusy || s.ttsEnabled ? "unmute" : "mute"}
            spin={ttsPreparing}
            className="tts-status-ic"
          />
          {ttsBusy && (
            <>
              <span className="tts-status-lbl">
                {ttsPreparing ? "音声を生成中" : "読み上げ中"}
                {ttsSource ? `・${ttsSource}` : ""}
                {ttsVoice ? `（${ttsVoice}）` : ""}
              </span>
              <Icon name="debug-stop" />
            </>
          )}
        </button>
        <button
          className="gear fs-toggle"
          title={fullscreen ? "全画面解除" : "全画面表示"}
          onClick={toggleFullscreen}
        >
          <Icon name={fullscreen ? "screen-normal" : "screen-full"} />
        </button>
        {/* PWA (standalone) 起動時はブラウザの再読み込みUIが無いので、代替のリロードボタンを出す。 */}
        {isStandalonePWA() && (
          <button className="gear reload-toggle" title="再読み込み" onClick={() => window.location.reload()}>
            <Icon name="refresh" />
          </button>
        )}
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
                <div className="ui-seg choice-seg acct-theme-seg">
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
        <NotificationCenter />
        {me ? (
          <div className="acct" ref={acctRef}>
            <button className="whoami acct-btn" title={me} onClick={() => setMenuOpen((o) => !o)}>
              <Icon name="account" /> <span className="acct-name">{me}</span>
              <Icon name="chevron-down" className="acct-caret" />
            </button>
            {menuOpen && (
              <div className="acct-menu" role="menu">
                <div className="acct-email" title={me}>{me}</div>
                {/* テナント選択はアカウントメニュー内に集約（上部バーの横幅を節約）。 */}
                {showPicker && (
                  <>
                    <label className="acct-tenant">
                      <span className="acct-tenant-lbl">テナント</span>
                      <select value={tenant} onChange={(e) => selectTenant(e.target.value)}>
                        {tenants.map((t) => (
                          <option key={t.slug} value={t.slug}>
                            {t.name} ({t.role})
                          </option>
                        ))}
                      </select>
                    </label>
                    <div className="acct-sep" />
                  </>
                )}
                {/* 初回カードを「あとで」で閉じたあとの再入口（起動導線 Ph1）。 */}
                <button className="acct-item" role="menuitem" onClick={() => run(openGuide)}>
                  <Icon name="rocket" /> はじめかたガイド
                </button>
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
                    <button
                      className="acct-item"
                      role="menuitem"
                      onClick={() => {
                        // Drop all client-side state BEFORE bouncing to the CP logout,
                        // so a different account on this browser can't see the prior
                        // user's layout / drafts / tenant selection.
                        clearLocalState();
                        location.assign(rel("oauth2/logout"));
                      }}
                    >
                      <Icon name="sign-out" /> ログアウト
                    </button>
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
