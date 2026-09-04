// Top bar — ported from the old components/TopBar.tsx (docs/log/22 P6b). Verbatim
// except the useApp() reads (→ core/store/tenant), the nav toggles (→ props from
// App) and openSettings/openAdmin (→ features/settings/store).
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useLayoutStore } from "../layout/store.ts";
import { useTtsStore, toggleTtsPlayback } from "../core/store/tts.ts";
import { useSettingsUI } from "../features/settings/store.ts";
import { useHostUpdate } from "../features/settings/hostUpdate.ts";
import { useDeploymentVersion, imageLabel } from "../features/settings/deploymentVersion.ts";
import { rel, clearLocalState } from "../core/api/client.ts";
import { useSettings, setSetting, THEMES, SURFACE_TARGETS, LOCALES, PANE_LAYOUTS } from "../lib/settings.ts";
import { useT, getLocale } from "../lib/i18n/index.ts";
import { useIsMobile, isStandalonePWA } from "../lib/device.ts";
import { buildInfo, buildLabel } from "../lib/version.ts";
import { Icon } from "../ui/Icon.tsx";
import { SwatchGrid } from "../ui/SwatchGrid.tsx";
import { useDismiss } from "../lib/useDismiss.ts";
import { NotificationCenter } from "../features/notifications/NotificationCenter.tsx";
import { hintSuffix } from "../features/keys/keyHint.ts";
import { confirmDirtyNavigation } from "../features/editor/dirtyRegistry.ts";

interface TopBarProps {
  toggleNav: () => void;
  toggleLeft: () => void;
  toggleLeftMode: () => void;
}

// Top bar: product name, tenant picker (hidden for single-membership users), and
// an account menu folding in the guides, settings, admin (super_admin only) and
// sign-out (oauth mode). The menu always shows so the guides / settings / build
// stay reachable even when no identity resolved (e.g. the native runtime has no
// login); the identity-dependent bits — the name in the button, the email header
// and sign-out — only render once an identity is present.
export function TopBar({ toggleNav, toggleLeft, toggleLeftMode }: TopBarProps) {
  const whoami = useTenantStore((s) => s.whoami);
  const tenants = useTenantStore((s) => s.tenants);
  const tenant = useTenantStore((s) => s.tenant);
  const showPicker = useTenantStore((s) => s.showPicker);
  const selectTenant = useTenantStore((s) => s.select);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  const s = useSettings();
  const tr = useT();
  const isMobile = useIsMobile();
  // Text-to-speech (docs/log/24): the pill is always shown. While speaking it reads
  // "speaking + stop" and a click stops the whole utterance; when idle it is the ON/OFF
  // toggle for the ttsEnabled setting. Voice session notifications (ttsSessionNotify) are
  // a separate axis and can speak while the setting is OFF, so speaking wins.
  const ttsSpeaking = useTtsStore((st) => st.speaking);
  const ttsPreparing = useTtsStore((st) => st.preparing);
  const ttsSource = useTtsStore((st) => st.source);
  const ttsVoice = useTtsStore((st) => st.voice);
  const ttsBusy = ttsSpeaking || ttsPreparing; // synthesis counts as playing for the pill
  // Hamburger: single-click toggles the left pane open/closed; double-click toggles
  // its desktop display mode (Push ⇄ overlay). We debounce the single action so a
  // double-click doesn't also fire it. Mobile keeps the immediate drawer toggle.
  const clickTimer = useRef<number | null>(null);
  // Drop the pending single-click delay on unmount, so toggleLeft cannot fire afterwards.
  useEffect(() => {
    return () => {
      if (clickTimer.current != null) window.clearTimeout(clickTimer.current);
    };
  }, []);
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
  const [apprOpen, setApprOpen] = useState(false); // appearance (theme + surface colors) popover
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

  // Close the account menu / appearance popover on an outside click or Escape. The
  // appearance popover is kept separate from the account menu so surface colors can be tuned in
  // a light popover while the panes stay visible behind it (a full settings modal
  // would hide the live preview).
  useDismiss(acctRef, menuOpen, () => setMenuOpen(false));
  useDismiss(apprRef, apprOpen, () => setApprOpen(false));

  const openSettings = useSettingsUI((st) => st.openSettings);
  const openAdmin = useSettingsUI((st) => st.openAdmin);
  const openTenantSettings = useSettingsUI((st) => st.openTenantSettings);
  // Native host self-update (docs/log/42): null on non-native deployments. When a newer
  // version is staged we surface it here — next to the build stamp — as a nudge into
  // Settings -> Toolchains, where the actual "restart and apply" action lives.
  const hostUpdate = useHostUpdate();
  const updateReady = !!hostUpdate?.restartRequired;
  // Deployment identity (version + images). Fetched only once the menu is opened —
  // nothing outside this menu shows it. The image half exists only where code arrives
  // as an image (ECS); elsewhere the CP omits the keys and these lines stay away.
  const deployment = useDeploymentVersion(menuOpen);
  const deployImages = !!(deployment?.image || deployment?.workspace_image);
  const [verCopied, setVerCopied] = useState(false);
  // One block with every fact a bug report needs — the point of the version zone
  // (docs/log/35 §35.6.1). Built at click time so it always matches what is on screen.
  const copyVersions = () => {
    const lines = [
      deployment?.version
        ? `Agent Fleet ${deployment.version}${deployment.runtime ? ` (${deployment.runtime})` : ""}`
        : "",
      deployment?.image ? `control-plane: ${imageLabel(deployment.image)}` : "",
      deployment?.workspace_image ? `workspace: ${imageLabel(deployment.workspace_image)}` : "",
      `console: ${buildLabel()}`,
    ].filter(Boolean);
    void navigator.clipboard?.writeText(lines.join("\n")).then(() => {
      setVerCopied(true);
      window.setTimeout(() => setVerCopied(false), 1500);
    });
  };
  const openGuide = useSettingsUI((st) => st.openGuide);
  // The guide ships per language: English is canonical (README.md), Japanese
  // lives beside it as README.ja.md — open the one matching the UI locale.
  //
  // The container receives the guide/ tree and nothing else, so the shipped layout
  // starts at the shelves (member/, admin/, …) — there is no docs/ level in it. The
  // tree's own README branches by reader, which is what someone clicking "User guide"
  // wants; every container has it (ADR 0064: the guide is not cut by role).
  const openUserGuide = () =>
    useLayoutStore.getState().openTargetInNew({
      content: {
        kind: "file",
        filePath: `/usr/local/share/agent-fleet/docs/README${getLocale() === "ja" ? ".ja" : ""}.md`,
      },
    }, true);
  const run = (fn: () => void) => {
    setMenuOpen(false);
    fn();
  };

  return (
    <header className="topbar">
      <div className="topbar-left">
        <button
          className="nav-toggle"
          title={tr("topbar.nav_toggle") + hintSuffix("workspace.toggleRail")}
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
              ? tr("topbar.tts.stop_off")
              : s.ttsEnabled
                ? tr("topbar.tts.on")
                : tr("topbar.tts.off")
          }
          // The combined stop+OFF logic is shared with the keyboard command (core/store/tts).
          onClick={toggleTtsPlayback}
        >
          {/* Before the first sound (still synthesising), spin to show it is generating */}
          <Icon
            name={ttsPreparing ? "loading" : ttsBusy || s.ttsEnabled ? "unmute" : "mute"}
            spin={ttsPreparing}
            className="tts-status-ic"
          />
          {ttsBusy && (
            <>
              <span className="tts-status-lbl">
                {ttsPreparing ? tr("topbar.tts.generating") : tr("topbar.tts.speaking")}
                {ttsSource ? `${tr("ui.sep")}${ttsSource}` : ""}
                {ttsVoice ? `（${ttsVoice}）` : ""}
              </span>
              <Icon name="debug-stop" />
            </>
          )}
        </button>
        <button
          className="gear fs-toggle"
          title={fullscreen ? tr("topbar.fullscreen_exit") : tr("topbar.fullscreen_enter")}
          onClick={toggleFullscreen}
        >
          <Icon name={fullscreen ? "screen-normal" : "screen-full"} />
        </button>
        {/* Launched as a standalone PWA there is no browser reload UI, so offer one here. */}
        {isStandalonePWA() && (
          <button
            className="gear reload-toggle"
            title={tr("topbar.reload")}
            onClick={() => {
              void confirmDirtyNavigation("reload").then((proceed) => {
                if (proceed) window.location.reload();
              });
            }}
          >
            <Icon name="refresh" />
          </button>
        )}
        {/* Appearance: a light popover so colors preview live on the panes behind it. */}
        <div className="acct appr" ref={apprRef}>
          <button
            className="gear appr-btn"
            title={tr("topbar.appearance_title")}
            onClick={() => setApprOpen((o) => !o)}
          >
            <Icon name="paintcan" />
          </button>
          {apprOpen && (
            <div className="acct-menu appr-menu" role="menu">
              <div className="appr-head">
                <div className="acct-email appr-head-title">{tr("topbar.appearance")}</div>
                <button
                  type="button"
                  className="appr-details"
                  role="menuitem"
                  title={tr("topbar.appearance_details_title")}
                  onClick={() => {
                    setApprOpen(false);
                    openSettings("display");
                  }}
                >
                  {tr("topbar.appearance_details")}
                </button>
              </div>
              <div className="acct-theme">
                <div className="appr-seg-row">
                  <span className="appr-seg-lbl">{tr("settings.language")}</span>
                  <div className="ui-seg choice-seg acct-theme-seg">
                    {LOCALES.map((l) => (
                      <button
                        key={l.id}
                        type="button"
                        className={"seg-btn" + (s.locale === l.id ? " active" : "")}
                        onClick={() => setSetting("locale", l.id)}
                      >
                        {l.label}
                      </button>
                    ))}
                  </div>
                </div>
                {/* Pane layout (split panes / tabbed grid) is not a color, but everything
                    about how the surfaces look should be settleable here, so it sits near
                    the top of the appearance popover. This only writes the same paneLayout
                    as Settings -> Display; the actual switch (including the confirmation
                    when there are unsaved edits) is App's loadMode. */}
                <div className="appr-seg-row">
                  <span className="appr-seg-lbl">{tr("display.pane_layout")}</span>
                  <div className="ui-seg choice-seg acct-theme-seg">
                    {PANE_LAYOUTS.map((p) => (
                      <button
                        key={p.id}
                        type="button"
                        className={"seg-btn" + (s.paneLayout === p.id ? " active" : "")}
                        onClick={() => setSetting("paneLayout", p.id)}
                      >
                        {tr(p.labelKey)}
                      </button>
                    ))}
                  </div>
                </div>
                <div className="appr-seg-row">
                  <span className="appr-seg-lbl">{tr("display.theme")}</span>
                  <div className="ui-seg choice-seg acct-theme-seg">
                    {THEMES.map((t) => (
                      <button
                        key={t.id}
                        type="button"
                        className={"seg-btn" + (s.theme === t.id ? " active" : "")}
                        onClick={() => setSetting("theme", t.id)}
                      >
                        {tr(t.labelKey)}
                      </button>
                    ))}
                  </div>
                </div>
                {SURFACE_TARGETS.map((t) => (
                  <SwatchRow key={t.key} label={tr(t.shortKey)} theme={s.theme} value={s[t.key]} onPick={(v) => setSetting(t.key, v)} />
                ))}
              </div>
            </div>
          )}
        </div>
        <NotificationCenter />
        <div className="acct" ref={acctRef}>
            <button
              className={"whoami acct-btn" + (updateReady ? " has-update" : "")}
              title={updateReady ? tr("topbar.update_ready", { v: hostUpdate!.installed }) : me || tr("topbar.menu")}
              onClick={() => setMenuOpen((o) => !o)}
            >
              <Icon name="account" /> {me && <span className="acct-name">{me}</span>}
              <Icon name="chevron-down" className="acct-caret" />
              {updateReady && (
                <span
                  className="acct-update-dot"
                  role="img"
                  aria-label={tr("topbar.update_ready", { v: hostUpdate!.installed })}
                />
              )}
            </button>
            {menuOpen && (
              <div className="acct-menu" role="menu">
                {me && <div className="acct-email" title={me}>{me}</div>}
                {/* Tenant selection lives inside the account menu to save top-bar width. */}
                {showPicker && (
                  <>
                    <label className="acct-tenant">
                      <span className="acct-tenant-lbl">{tr("topbar.tenant")}</span>
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
                <button className="acct-item" role="menuitem" onClick={() => run(openUserGuide)}>
                  <Icon name="book" /> {tr("topbar.user_guide")}
                </button>
                {/* Way back in after dismissing the first-run card with "later" (launch flow Ph1). */}
                <button className="acct-item" role="menuitem" onClick={() => run(openGuide)}>
                  <Icon name="rocket" /> {tr("topbar.guide")}
                </button>
                <button className="acct-item" role="menuitem" onClick={() => run(() => openSettings())}>
                  <Icon name="gear" /> {tr("topbar.settings")}
                </button>
                {/* Tenant settings are the surface for tenants you administer, so membership
                    normally decides the entry point — but super_admin gets it too. The server
                    has always let super_admin through as an admin of any tenant
                    (`tenantAdminFor`), so hiding the item hid only the door, and that door was
                    the sole way into surfaces that exist nowhere else (source-address
                    restriction, docs/log/66) for a super_admin with no membership. With more
                    than one tenant the modal shows its own picker (TenantDialog). */}
                {(superAdmin || tenants?.some((t) => t.role === "tenant_admin")) && (
                  <button className="acct-item" role="menuitem" onClick={() => run(() => openTenantSettings())}>
                    <Icon name="organization" /> {tr("topbar.tenant_settings")}
                  </button>
                )}
                {/* Admin is the deployment-wide surface (creating tenants, limits, login
                    rules, egress, hosts, the TTS dictionary) and the CP pins every one of
                    those to withSuperAdmin. The surfaces that also mean something to a
                    tenant_admin (members, sessions, usage, audit, MCP distribution) moved to
                    tenant settings, so super_admin alone is right here. Hiding it is not the
                    permission check: the server has always returned 403. */}
                {superAdmin && (
                  <button className="acct-item" role="menuitem" onClick={() => run(openAdmin)}>
                    <Icon name="shield" /> {tr("topbar.admin")}
                  </button>
                )}
                {canLogout && (
                  <>
                    <div className="acct-sep" />
                    <button
                      className="acct-item"
                      role="menuitem"
                      onClick={() => {
                        void confirmDirtyNavigation("logout").then((proceed) => {
                          if (!proceed) return;
                          // Drop all client-side state BEFORE bouncing to the CP logout,
                          // so a different account on this browser can't see the prior
                          // user's layout / drafts / tenant selection.
                          clearLocalState();
                          location.assign(rel("oauth2/logout"));
                        });
                      }}
                    >
                      <Icon name="sign-out" /> {tr("topbar.logout")}
                    </button>
                  </>
                )}
                {/* Version zone. Native host self-update (docs/log/42) sits above the FE
                    build stamp: a CTA when a newer af is staged (-> Settings for the restart),
                    else the current host version. Both hidden on non-native (hostUpdate
                    null). The build stamp below is the Console FRONTEND bundle — a
                    separate thing — so each carries its own label. */}
                <div className="acct-sep" />
                {hostUpdate &&
                  (updateReady ? (
                    <button
                      className="acct-item acct-update"
                      role="menuitem"
                      onClick={() => run(() => openSettings("env"))}
                    >
                      <Icon name="cloud-download" />
                      <span className="acct-update-txt">{tr("topbar.update_ready", { v: hostUpdate.installed })}</span>
                      <span className="acct-update-badge">{tr("topbar.update_badge")}</span>
                    </button>
                  ) : (
                    <div className="acct-build">
                      <Icon name="rocket" /> {tr("topbar.host_version", { v: hostUpdate.current })}
                    </div>
                  ))}
                {/* Deployment identity, ECS only (see useDeploymentVersion): on a
                    deployment where code ships as an image, "which version" and "which
                    image" are two different questions and a report needs both — the CP
                    and the workspace share one ImageTag by convention, but a rollback
                    can move just one of them. The digest rides along because `:dev` is
                    mutable. Nothing here is compared against anything: backend drift is
                    the WS-bar "restart required" badge's job, and a second opinion here would be
                    the version-comparison trap workspace_stale.go warns about. */}
                {deployImages && (
                  <>
                    <div className="acct-build">
                      <Icon name="rocket" /> {tr("topbar.server_version", { v: deployment!.version })}
                    </div>
                    {deployment!.image && (
                      <div className="acct-build" title={deployment!.image.digest}>
                        <Icon name="package" /> {tr("topbar.image_cp", { ref: imageLabel(deployment!.image) })}
                      </div>
                    )}
                    {deployment!.workspace_image && (
                      <div className="acct-build" title={deployment!.workspace_image.digest}>
                        <Icon name="package" />{" "}
                        {tr("topbar.image_ws", { ref: imageLabel(deployment!.workspace_image) })}
                      </div>
                    )}
                  </>
                )}
                {/* Build stamp — so the running version is visible at a glance (no more
                    guessing which build a phone is on). Selectable for easy reporting,
                    with a copy button that takes the whole zone in one go (typing a
                    digest off a phone screen is how reports end up without one). */}
                <div className="acct-build" title={buildInfo.sha ? `commit ${buildInfo.sha}` : undefined}>
                  <Icon name="tag" /> {tr("topbar.build", { label: buildLabel() })}
                  <button
                    type="button"
                    className="acct-ver-copy"
                    title={tr("topbar.copy_version")}
                    aria-label={tr("topbar.copy_version")}
                    onClick={copyVersions}
                  >
                    <Icon name={verCopied ? "check" : "copy"} />
                  </button>
                </div>
              </div>
            )}
        </div>
      </div>
    </header>
  );
}

// SwatchRow: a labeled row wrapping the shared SwatchGrid (surface-color picker) for
// the appearance popover. Tapping applies immediately and keeps the popover open.
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
