// App shell for the next console — P1: terminal + layout core (docs/22).
//
// The main area is the real PaneHost (split panes + live xterm PTYs) wired to
// the zustand stores; the rail carries a provisional sessions list (full
// SessionsSection lands in P2). Boot order: resolve tenant → per-tenant layout
// restore (with old-format migration) → per-user display prefs → workspace +
// sessions polling. History (back/forward) traverses layout states.
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useT } from "../lib/i18n/index.ts";
import { startPushChannel, restartPush } from "../core/push/events.ts";
import { wirePushApply } from "../core/push/wire.ts";
import { useWorkspaceStore, startWorkspacePolling } from "../core/store/workspace.ts";
import { useLayoutStore, wireLayoutHistory } from "../layout/store.ts";
import { wireKeys } from "../features/keys/dispatcher.ts";
import { useLeftRail } from "../core/store/leftRail.ts";
import { wireTerminalReconcile } from "../terminal/service.ts";
import { disposeAllBrowsers, resetBrowserRuntime, wireBrowserReconcile } from "../features/browser/service.ts";
import {
  disposeAllBrowserAttachments,
  wireBrowserAttachmentReconcile,
} from "../features/browser/attachmentService.ts";
import { useSessionsStore, startSessionsPolling } from "../features/sessions/store.ts";
import { SessionModals } from "../features/sessions/SessionModals.tsx";
import { AuthExpiredModal } from "../features/auth/AuthExpiredModal.tsx";
import { WsStartingDialog } from "./WsStartingDialog.tsx";
import { useSessionNotifications } from "../features/sessions/useSessionNotifications.ts";
import { useReposStore, startReposPolling } from "../features/repos/store.ts";
import { useFilesStore } from "../features/files/store.ts";
import { useChatStore, startChatPolling } from "../features/chat/store.ts";
import { hydrateUIPrefs, refreshUIPrefs, setSetting, useSettings } from "../lib/settings.ts";
import { MOBILE_QUERY, coarsePointer } from "../lib/device.ts";
import { PaneHost } from "../features/panes/PaneHost.tsx";
import { LayoutMap } from "../features/panes/LayoutMap.tsx";
import { WorkingSetBar } from "./WorkingSetBar.tsx";
import { AssistantSection } from "../features/chat/AssistantSection.tsx";
import { MemoQueueSection } from "../features/memo/MemoQueueSection.tsx";
import { SchedulesSection } from "../features/schedules/SchedulesSection.tsx";
import { ProjectTree } from "../features/project/ProjectTree.tsx";
import { OtherSessionsSection } from "../features/project/OtherSessionsSection.tsx";
import { StoppedSessionsSection } from "../features/project/StoppedSessionsSection.tsx";
import { FilesSection } from "../features/project/FilesSection.tsx";
import { Section } from "../ui/Section.tsx";
import { WsBar } from "./WsBar.tsx";
import { TopBar } from "./TopBar.tsx";
import { useSettingsUI, wireSettingsHistory } from "../features/settings/store.ts";
import { SettingsDialog } from "../features/settings/SettingsDialog.tsx";
import { AdminDialog } from "../features/settings/AdminDialog.tsx";
import { GuideModal } from "../features/terminal/OnboardingCard.tsx";
import { StartHost } from "../features/repos/StartHost.tsx";
import { startNotificationPolling, useNotificationStore, wireNotificationReadOnActiveSession } from "../features/notifications/store.ts";
import { WhichKey } from "../features/keys/WhichKey.tsx";
import { CommandPalette } from "../features/keys/CommandPalette.tsx";
import { CheatSheet } from "../features/keys/CheatSheet.tsx";
import { useUpdateCheck } from "../lib/useUpdateCheck.tsx";
import { consumeSessionDeepLink } from "../lib/sessionDeepLink.ts";
import { usePopoutMode } from "../lib/popoutMode.ts";
import { takePendingPopout, takeStalePopoutLink } from "../features/panes/popout.ts";
import type { PopoutDescriptor } from "../layout/popout.ts";
import { confirmDirtyNavigation } from "../features/editor/dirtyRegistry.ts";
import { PopoutTitleBar } from "../features/panes/PopoutTitleBar.tsx";
import { toast } from "../ui/toast.ts";
import { t } from "../lib/i18n/index.ts";
import { DirtyGuardHost } from "../features/editor/DirtyGuardHost.tsx";
import { browserAttachmentIdFromPath } from "../layout/browserAttachmentAction.ts";
import { openBrowserAttachment } from "../features/browser/attachmentAction.ts";

// Refresh FILES (and repos/sessions/chat list on start) whenever the workspace
// actually flips running↔stopped — including external changes the 4s sync catches
// (admin stop, OOM, restart). Keyed on the transition, and on the RUNNING edge (not
// the "starting…" click), so trees load once the agent is really up. Transient "…"
// states — and the server-reported "starting" (ECS cold pull) — are unsettled and
// ignored, so the refresh fires exactly once on the starting→running flip.
// Returns the unsubscribe (StrictMode-safe).
function wireWorkspaceRefresh(): () => void {
  const settle = (s: string) => (s === "running" ? "running" : s === "none" || s === "stopped" ? "stopped" : "");
  let prevRaw = useWorkspaceStore.getState().state;
  return useWorkspaceStore.subscribe((s) => {
    const from = settle(prevRaw);
    const to = settle(s.state);
    prevRaw = s.state;
    if (to === "" || to === from) return;
    resetBrowserRuntime(to === "running");
    useFilesStore.getState().bump();
    if (to === "running") {
      void useReposStore.getState().refresh();
      void useSessionsStore.getState().refresh();
      // Chat conversations are proxied into the workspace agent, so the list fails
      // while it's still starting — re-fetch once it's really up.
      useChatStore.getState().bumpList();
    }
  });
}

export function App() {
  const tr = useT();
  const tenant = useTenantStore((s) => s.tenant);
  const identityRev = useTenantStore((s) => s.identityRev);
  // Deployment gate: only show the schedules rail when the CP scheduler is enabled
  // (AF_SCHEDULER_INTERVAL set) — otherwise schedules can never fire, so hide the section.
  // An UNRESOLVED identity (whoami errored / the CP was down at boot) fails OPEN: hiding a
  // whole feature because a capability flag could not be read reads as "my schedules are
  // gone", which is worse than showing a section the deployment may not drive. Only an
  // answered whoami can hide it — and that answer is re-read on push reconnect (wire.ts),
  // so enabling the scheduler no longer needs a browser reload.
  const schedulerEnabled = useTenantStore((s) => (s.whoami ? !!s.whoami.scheduler_enabled : true));
  const layout = useLayoutStore((s) => s.layout);
  const paneLayout = useSettings().paneLayout;
  const settingsOpen = useSettingsUI((s) => s.settingsOpen);
  const adminOpen = useSettingsUI((s) => s.adminOpen);
  const guideOpen = useSettingsUI((s) => s.guideOpen);
  const [booted, setBooted] = useState(false);
  const notificationSource = useNotificationStore((s) => s.sourceState);
  const workspaceRunning = useWorkspaceStore((s) => s.state) === "running";
  // Pop-out tab (ペインの別タブ切り離し): "popout" renders minimal chrome below;
  // "full" is a normal console seeded with the popped pane (no branch needed).
  const popout = usePopoutMode();
  // Pop-out seed descriptor (consumed once at boot) + the identityRev the layout
  // was last loaded under — bookkeeping for the per-tenant sync effect and the
  // identity-reload effect below.
  const popoutSeedRef = useRef<PopoutDescriptor | null>(null);
  const identityRevDoneRef = useRef(0);
  const browserAttachmentActionHandledRef = useRef(false);

  // Detect a newer deployed build and offer a one-tap, cache-busting reload.
  useUpdateCheck();

  // Left rail visibility. Desktop: leftOpen (persisted) + leftMode "push" (docks,
  // main reflows) / "overlay" (floats above main). Mobile (≤760px): the rail is an
  // off-canvas drawer driven by navOpen; selecting anything closes it.
  const [navOpen, setNavOpen] = useState(false);
  const navOpenRef = useRef(navOpen);
  navOpenRef.current = navOpen;
  // Desktop rail dock/collapse + push/overlay mode now live in a store so the Ctrl+B
  // keyboard command can drive them from outside React (core/store/leftRail). The
  // tablet edge-swipe's transient "float as overlay" flag (swipeOverlay) lives there
  // too, so open/close from any path (swipe / hamburger / Ctrl+B) stays consistent.
  const leftOpen = useLeftRail((s) => s.open);
  const leftMode = useLeftRail((s) => s.mode);
  const swipeOverlay = useLeftRail((s) => s.swipeOverlay);
  const toggleLeft = useLeftRail((s) => s.toggle);
  const closeLeft = useLeftRail((s) => s.close);
  const toggleLeftMode = useLeftRail((s) => s.toggleMode);
  const toggleNav = () => setNavOpen((o) => !o);

  // Any navigation (layout change) closes the mobile drawer so the main area
  // comes forward — covers every open-path without threading closeNav around.
  // popstate-driven layout changes are exempt: there the drawer flag from the
  // history entry decides (popNavRef), so back can REOPEN the drawer.
  const popNavRef = useRef<boolean | null>(null);
  useEffect(() => {
    if (popNavRef.current !== null) {
      popNavRef.current = null;
      return;
    }
    setNavOpen(false);
  }, [layout]);

  // Drawer ↔ history integration. Opening the mobile drawer pushes a {drawer:true}
  // entry so the device/browser back button CLOSES the drawer instead of leaving the
  // page (which otherwise trips the terminal's beforeunload guard). Deriving the
  // "are we on a drawer entry" from history.state keeps push/consume balanced across
  // both UI toggles and popstate:
  //   - open by UI on a non-drawer entry → push a guard entry
  //   - close by UI while still on the drawer entry (backdrop / swipe / hamburger)
  //     → history.back() consumes it
  //   - navigating away from an open drawer leaves the guard entry buried, so a later
  //     back reopens the drawer over the previous view (old pushDrawerEntry behavior)
  //   - popstate-driven open/close already sits on the right entry → both no-op
  const drawerHistSynced = useRef(false);
  useEffect(() => {
    // 初回マウントはスキップ — ドロワー entry 上でリロードすると history.state.drawer が
    // 残っており、閉状態の初期実行が history.back() を誤発火して 1 段戻ってしまう。
    // 同期するのは実際の open/close 遷移だけでよい。
    if (!drawerHistSynced.current) {
      drawerHistSynced.current = true;
      return;
    }
    const onDrawerEntry = !!(history.state && history.state.drawer);
    const onMobile = window.matchMedia(MOBILE_QUERY).matches;
    if (navOpen && onMobile && !onDrawerEntry) {
      try {
        history.pushState({ __af: true, layout: useLayoutStore.getState().layout, drawer: true }, "");
      } catch {}
    } else if (!navOpen && onDrawerEntry) {
      try {
        history.back();
      } catch {}
    }
  }, [navOpen]);

  // popstate restores navOpen from the entry's drawer flag; popNavRef tells the
  // "close on layout change" effect above to skip (so back can REOPEN the drawer).
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const open = !!(e.state && e.state.drawer);
      popNavRef.current = open;
      setNavOpen(open);
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // Edge swipe reveals the left rail. Phone (≤760px): drives the off-canvas
  // drawer (navOpen). Tablet (>760px, touch): drives the desktop rail (leftOpen),
  // floated as an overlay for that reveal (see leftRail.openOverlay). Swipe right from
  // the left third opens; swipe left closes. Mouse desktops never fire TouchEvent,
  // so they're inert. Passive listeners; vertical drags are left for scrolling.
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    const DIST = 50;
    // A drawer gesture is a quick swipe.  Cancelling the candidate after the
    // browser's long-press window keeps text selection (notably in the mirror)
    // from turning a later selection-handle drag into an edge swipe.
    const LONG_PRESS_MS = 500;
    let sx = 0,
      sy = 0,
      mode: "open" | "close" | null = null,
      // Which surface this gesture drives, latched at touchstart: the phone
      // drawer (navOpen) vs. the tablet desktop rail (leftOpen overlay).
      drawer = false,
      longPressTimer: number | null = null;
    const cancelGesture = () => {
      mode = null;
      if (longPressTimer !== null) {
        window.clearTimeout(longPressTimer);
        longPressTimer = null;
      }
    };
    // ローカル変数は touch — i18n の t（このモジュールで import 済み）を隠さない名前に。
    const onStart = (e: TouchEvent) => {
      const touch = e.touches[0];
      cancelGesture();
      const phone = mq.matches;
      // Above the phone breakpoint, only enable the gesture on touch devices
      // (tablets) — a mouse desktop shouldn't get an edge-swipe rail.
      const tablet = !phone && coarsePointer();
      drawer = phone;
      // While a modal is up, don't let an edge swipe open the rail behind it.
      const { settingsOpen, adminOpen } = useSettingsUI.getState();
      if (touch && (phone || tablet) && !settingsOpen && !adminOpen) {
        const isOpen = phone ? navOpenRef.current : useLeftRail.getState().open;
        if (isOpen) mode = "close";
        else if (touch.clientX < Math.min(window.innerWidth * 0.33, 160)) mode = "open";
      }
      if (touch) {
        sx = touch.clientX;
        sy = touch.clientY;
        if (mode) {
          longPressTimer = window.setTimeout(cancelGesture, LONG_PRESS_MS);
        }
      }
    };
    const onMove = (e: TouchEvent) => {
      if (!mode) return;
      const touch = e.touches[0];
      if (!touch) return;
      const dx = touch.clientX - sx;
      const dy = touch.clientY - sy;
      if (Math.abs(dx) <= Math.abs(dy)) return;
      if (mode === "open" && dx > DIST) {
        if (drawer) setNavOpen(true);
        else useLeftRail.getState().openOverlay();
        cancelGesture();
      } else if (mode === "close" && dx < -DIST) {
        if (drawer) setNavOpen(false);
        else useLeftRail.getState().close();
        cancelGesture();
      }
    };
    const onEnd = cancelGesture;
    window.addEventListener("touchstart", onStart, { passive: true });
    window.addEventListener("touchmove", onMove, { passive: true });
    window.addEventListener("touchend", onEnd, { passive: true });
    window.addEventListener("touchcancel", onEnd, { passive: true });
    return () => {
      window.removeEventListener("touchstart", onStart);
      window.removeEventListener("touchmove", onMove);
      window.removeEventListener("touchend", onEnd);
      window.removeEventListener("touchcancel", onEnd);
      cancelGesture();
    };
  }, []);

  // One-time wiring: history (back/forward → layout), terminal reconciliation,
  // pollers. All return cleanups, so StrictMode's double-invoke is safe.
  useEffect(() => {
    let alive = true;
    let prefsReady = false;
    let prefsRefreshing = false;
    // ui-prefs are server-backed, but a phone/PWA can keep this App instance alive for
    // days. Rehydrate when it returns to the foreground so changes made on another
    // device (for example Claude's registered models) appear without a hard reload.
    const refreshPrefs = () => {
      if (!alive || !prefsReady || prefsRefreshing) return;
      prefsRefreshing = true;
      void refreshUIPrefs().finally(() => { prefsRefreshing = false; });
    };
    const onPrefsVisible = () => {
      if (document.visibilityState === "visible") refreshPrefs();
    };
    document.addEventListener("visibilitychange", onPrefsVisible);
    window.addEventListener("focus", refreshPrefs);
    const unHistory = wireLayoutHistory();
    const unModalHistory = wireSettingsHistory();
    const unKeys = wireKeys();
    const unReconcile = wireTerminalReconcile();
    const unBrowserReconcile = wireBrowserReconcile();
    const unBrowserAttachmentReconcile = wireBrowserAttachmentReconcile();
    const unWsRefresh = wireWorkspaceRefresh();
    // 統合 push チャネル（通信量削減 P3）: 配線を先に登録してから接続 — 初回
    // スナップショットのフレームを取りこぼさない。ポーラーはフォールバックで
    // そのまま起動する（pushHealthy 中は tick スキップ）。
    const unPushApply = wirePushApply();
    const stopPush = startPushChannel();
    const stopWsPoll = startWorkspacePolling();
    const stopSessPoll = startSessionsPolling();
    const stopReposPoll = startReposPolling();
    const stopChatPoll = startChatPolling();
    const stopNotificationPoll = startNotificationPolling();
    const unNotificationRead = wireNotificationReadOnActiveSession();
    void (async () => {
      await useTenantStore.getState().init();
      await hydrateUIPrefs();
      if (!alive) return;
      prefsReady = true;
      setBooted(true);
    })();
    // Chat-bridge notification links (?session=<name>) open that session's pane.
    // Idempotent across StrictMode's double-invoke: the first call strips the param.
    consumeSessionDeepLink();
    return () => {
      alive = false;
      document.removeEventListener("visibilitychange", onPrefsVisible);
      window.removeEventListener("focus", refreshPrefs);
      unHistory();
      unModalHistory();
      unKeys();
      unReconcile();
      unBrowserReconcile();
      unBrowserAttachmentReconcile();
      unWsRefresh();
      unPushApply();
      stopPush();
      stopWsPoll();
      stopSessPoll();
      stopReposPoll();
      stopChatPoll();
      stopNotificationPoll();
      unNotificationRead();
    };
  }, []);

  // Per-tenant sync: on boot completion AND on tenant switch — restore that
  // tenant's saved split (migrating the old console's format on first load)
  // and refetch tenant-scoped data.
  useEffect(() => {
    if (!booted) return;
    // pane ids are tab-local, not tenant-global. Never carry an ephemeral Page
    // owned by the previous membership into a same-named pane in the next tenant.
    disposeAllBrowsers();
    disposeAllBrowserAttachments();
    // 旧テナントの push ストリームを先に落としてから通知ストアを reset する —
    // 逆順だと reset 後に旧テナントのフレームが 1 個滑り込みうる。
    restartPush();
    useNotificationStore.getState().reset();
    void useNotificationStore.getState().refresh();
    // Pop-out first boot: seed the layout from the handed-off descriptor
    // instead of restoring the saved split. Handed over exactly once —
    // reloads and tenant switches fall back to the normal load(). The descriptor
    // is kept in a ref so the identity-reload effect below can re-seed the same
    // pane under the re-scoped layout key.
    const popped = takePendingPopout();
    if (popped) {
      popoutSeedRef.current = popped;
      useLayoutStore.getState().initSinglePane(popped.content, popped.session, popped.wrap);
    } else {
      useLayoutStore.getState().load(tenant);
    }
    // This run loaded under the CURRENT identity — mark its rev as handled so the
    // identity-reload effect doesn't double-load right after boot.
    identityRevDoneRef.current = useTenantStore.getState().identityRev;
    if (takeStalePopoutLink()) toast(t("popout.stale_link"), { kind: "info" });
    void useWorkspaceStore.getState().refresh();
    void useSessionsStore.getState().refresh();
  }, [booted, tenant]);

  // The preference chooses a profile, not a conversion: each profile retains
  // its own tab-local layout so switching never destroys terminals or drafts.
  useEffect(() => {
    if (!booted || layout.mode === paneLayout) return;
    void confirmDirtyNavigation("layout").then((proceed) => {
      if (proceed) useLayoutStore.getState().loadMode(tenant, paneLayout);
      else setSetting("paneLayout", layout.mode === "tabs" ? "tabs" : "split");
    });
  }, [booted, tenant, paneLayout, layout.mode]);

  // A Chromium attachment changes layout only after the user has followed its
  // action URL. MCP/server activity alone never reaches this effect. It runs
  // after the tenant layout load above, then verifies membership/expiry before
  // calculating one commit from the current layout.
  useEffect(() => {
    if (!booted || browserAttachmentActionHandledRef.current) return;
    const attachmentId = browserAttachmentIdFromPath(location.pathname);
    if (!attachmentId) return;
    browserAttachmentActionHandledRef.current = true;
    void openBrowserAttachment(attachmentId, { fromActionURL: true });
  }, [booted, tenant]);

  // Layout re-load ONLY — deliberately narrower than the per-tenant sync above.
  // A DELAYED whoami resolution (CP transient 5xx at boot, a retry landed later)
  // re-scoped the layout key via setUser, so the layout loaded under the shared
  // no-user key must be re-read under the user key (otherwise it would be
  // persisted into it). Everything else in the sync effect must NOT re-run here:
  // disposeAllBrowsers() would kill live browser pages, restartPush()+reset()
  // would drop unread notifications, and a pop-out tab (descriptor consumed at
  // boot) would fall through to load() and lose its detached pane.
  useEffect(() => {
    if (!booted || identityRev === identityRevDoneRef.current) return;
    identityRevDoneRef.current = identityRev;
    void confirmDirtyNavigation("layout").then((proceed) => {
      if (!proceed) return; // keep the shared-key layout rather than drop unsaved buffers
      const popped = popoutSeedRef.current;
      if (popped) useLayoutStore.getState().initSinglePane(popped.content, popped.session, popped.wrap);
      else useLayoutStore.getState().load(tenant);
    });
  }, [booted, tenant, identityRev]);

  // Desktop notifications on claude state arrivals — lives at the shell now that
  // the flat Sessions section no longer owns the rail. A minimal pop-out tab
  // suppresses them: the main console tab already fires the same notifications,
  // so a satellite tab would just duplicate every ping.
  useSessionNotifications(notificationSource === "unsupported" && popout !== "popout");

  // Minimal pop-out chrome: title bar + the (1-pane) PaneHost, plus the overlay
  // layer (dialogs the reduced command set can still reach + auth/workspace
  // modals). No rail / TopBar / WsBar — 展開 (PopoutTitleBar) converts in place.
  if (popout === "popout") {
    return (
      <div className="app-shell popout">
        <PopoutTitleBar />
        <main className="app-main">
          <PaneHost />
        </main>
        {settingsOpen && <SettingsDialog />}
        {adminOpen && <AdminDialog />}
        {guideOpen && <GuideModal />}
        <SessionModals />
        <WsStartingDialog />
        <AuthExpiredModal />
        <DirtyGuardHost />
        <WhichKey />
        <CommandPalette />
        <CheatSheet />
      </div>
    );
  }

  return (
    <div
      className={
        "app-shell" +
        (leftOpen ? "" : " left-collapsed") +
        (leftMode === "overlay" || swipeOverlay ? " left-overlay" : "") +
        (navOpen ? " nav-open" : "")
      }
    >
      <TopBar toggleNav={toggleNav} toggleLeft={toggleLeft} toggleLeftMode={toggleLeftMode} />
      <WsBar />
      <div className="app-body">
        {/* Desktop mouse-only: hovering the left edge while the rail is
            collapsed peeks it open, mirroring the tablet edge-swipe below.
            Hidden via CSS (hover:hover + pointer:fine) on touch/mobile, so
            this never fires from a tap there. */}
        <div
          className="app-edge-hotzone"
          onMouseEnter={() => {
            if (!useLeftRail.getState().open) useLeftRail.getState().openOverlay();
          }}
        />
        <nav className="app-rail">
          <LayoutMap />
          {/* 作業グループ (docs/52): pinned OUTSIDE the scroll area so the active
              scope stays visible however far the rail scrolls — rendered for the
              stopped rail too (the switcher works without the agent). */}
          <WorkingSetBar />
          {/* Project-first IA: Assistant + Memo pinned on top (global tools), then
              the repo tree (each base node nests its sessions + worktrees), then
              the repo-less session catch-all, and the global file browser at the
              foot (default collapsed; a reveal opens it). */}
          <div className="app-rail-scroll">
            {workspaceRunning ? (
              <>
                <AssistantSection />
                <MemoQueueSection />
                {schedulerEnabled && <SchedulesSection />}
                <ProjectTree />
                <OtherSessionsSection />
                <FilesSection />
              </>
            ) : (
              <>
                <StoppedRailSection id="assistant" title={tr("ui.assistant")} icon="comment-discussion" />
                <MemoQueueSection />
                {schedulerEnabled && <SchedulesSection />}
                <StoppedRailSection id="repos" title={tr("ui.repositories")} icon="repo" />
                <StoppedSessionsSection />
                <StoppedRailSection id="files" title={tr("ui.files")} icon="files" defaultOpen={false} />
              </>
            )}
          </div>
        </nav>
        {/* Dims the main area and dismisses the pane: the mobile drawer, and the
            desktop overlay-mode left pane. */}
        <div
          className="app-nav-backdrop"
          onClick={() => {
            setNavOpen(false);
            closeLeft();
          }}
        />
        <main className="app-main">
          <PaneHost />
        </main>
      </div>
      {settingsOpen && <SettingsDialog />}
      {adminOpen && <AdminDialog />}
      {guideOpen && <GuideModal />}
      <StartHost />
      <SessionModals />
      <WsStartingDialog />
      <AuthExpiredModal />
      <DirtyGuardHost />
      <WhichKey />
      <CommandPalette />
      <CheatSheet />
    </div>
  );
}

function StoppedRailSection({
  id,
  title,
  icon,
  defaultOpen = true,
}: {
  id: string;
  title: string;
  icon: string;
  defaultOpen?: boolean;
}) {
  const tr = useT();
  return (
    <Section id={id} title={title} icon={icon} defaultOpen={defaultOpen}>
      <div className="section-empty">{tr("ui.starts_when_workspace_running")}</div>
    </Section>
  );
}
