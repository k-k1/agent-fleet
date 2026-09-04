// App shell for the next console — P1: terminal + layout core (docs/log/22).
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
import { wireAttentionBeacon } from "../lib/attention.ts";
import { disposeAllBrowsers, resetBrowserRuntime, wireBrowserReconcile } from "../features/browser/service.ts";
import {
  disposeAllBrowserAttachments,
  wireBrowserAttachmentReconcile,
} from "../features/browser/attachmentService.ts";
import { useSessionsStore, startSessionsPolling } from "../features/sessions/store.ts";
import { SessionModals } from "../features/sessions/SessionModals.tsx";
import { AuthExpiredModal } from "../features/auth/AuthExpiredModal.tsx";
import { ProviderRequiredModal } from "../features/auth/ProviderRequiredModal.tsx";
import { NotProvisioned } from "../features/auth/NotProvisioned.tsx";
import { WsStartingDialog } from "./WsStartingDialog.tsx";
import { useSessionNotifications } from "../features/sessions/useSessionNotifications.ts";
import { useReposStore, startReposPolling } from "../features/repos/store.ts";
import { startRepoJobsPolling } from "../features/repos/jobs.ts";
import { useFilesStore } from "../features/files/store.ts";
import { wireFilesSessionRefresh } from "../features/files/sessionRefresh.ts";
import { useChatStore, startChatPolling } from "../features/chat/store.ts";
import { hydrateUIPrefs, refreshUIPrefs, resyncAccumulatedForIdentitySwitch, setSetting, useSettings } from "../lib/settings.ts";
import { MOBILE_QUERY, coarsePointer } from "../lib/device.ts";
import { PaneHost } from "../features/panes/PaneHost.tsx";
import { LayoutMap } from "../features/panes/LayoutMap.tsx";
import { WorkingSetBar } from "./WorkingSetBar.tsx";
import { AssistantSection } from "../features/chat/AssistantSection.tsx";
import { MemoQueueSection } from "../features/memo/MemoQueueSection.tsx";
import { WorkItemsSection } from "../features/workitems/WorkItemsSection.tsx";
import { SchedulesSection } from "../features/schedules/SchedulesSection.tsx";
import { ProjectTree } from "../features/project/ProjectTree.tsx";
import { OtherSessionsSection } from "../features/project/OtherSessionsSection.tsx";
import { StoppedSessionsSection } from "../features/project/StoppedSessionsSection.tsx";
import { SharedSessionsSection } from "../features/sharing/SharedSessionsSection.tsx";
import { FilesSection } from "../features/project/FilesSection.tsx";
import { Section } from "../ui/Section.tsx";
import { WsBar } from "./WsBar.tsx";
import { TopBar } from "./TopBar.tsx";
import { useSettingsUI } from "../features/settings/store.ts";
import { SettingsDialog } from "../features/settings/SettingsDialog.tsx";
import { AdminDialog } from "../features/settings/AdminDialog.tsx";
import { TenantDialog } from "../features/settings/TenantDialog.tsx";
import { GuideModal } from "../features/terminal/OnboardingCard.tsx";
import { StartHost } from "../features/repos/StartHost.tsx";
import { startNotificationPolling, useNotificationStore, wireNotificationReadOnActiveSession } from "../features/notifications/store.ts";
import { WhichKey } from "../features/keys/WhichKey.tsx";
import { CommandPalette } from "../features/keys/CommandPalette.tsx";
import { CheatSheet } from "../features/keys/CheatSheet.tsx";
import { useUpdateCheck } from "../lib/useUpdateCheck.tsx";
import { consumeSessionDeepLink } from "../lib/sessionDeepLink.ts";
import { popoutMode, usePopoutMode } from "../lib/popoutMode.ts";
import { installSwipeGestures } from "./swipeGestures.ts";
import { rotateRunningSession } from "../features/sessions/open.ts";
import { displayName } from "../lib/sessionview.ts";
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

// Phone horizontal swipe: advance the running session by one (left = next, right =
// previous). The whole screen changes, so a short toast reports where it landed (which
// of how many). A no-op (only this session, or none) says why rather than dropping
// silently.
function rotateToSession(delta: number): void {
  const target = rotateRunningSession(delta);
  if (!target) {
    toast(t("swipe.rotate_none"), { kind: "info", duration: 2000 });
    return;
  }
  toast(
    t("swipe.rotated", {
      n: target.index + 1,
      total: target.total,
      name: displayName(target.session),
    }),
    { kind: "info", duration: 1600 },
  );
}

export function App() {
  const tr = useT();
  const tenant = useTenantStore((s) => s.tenant);
  const identityRev = useTenantStore((s) => s.identityRev);
  const notProvisioned = useTenantStore((s) => s.notProvisioned);
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
  const tenantOpen = useSettingsUI((s) => s.tenantOpen);
  const guideOpen = useSettingsUI((s) => s.guideOpen);
  const [booted, setBooted] = useState(false);
  const notificationSource = useNotificationStore((s) => s.sourceState);
  const workspaceRunning = useWorkspaceStore((s) => s.state) === "running";
  // Pop-out tab (a pane detached into its own tab): "popout" renders minimal chrome below;
  // "full" is a normal console seeded with the popped pane (no branch needed).
  const popout = usePopoutMode();
  // Pop-out seed descriptor (consumed once at boot) + the identityRev the layout
  // was last loaded under — bookkeeping for the per-tenant sync effect and the
  // identity-reload effect below.
  const popoutSeedRef = useRef<PopoutDescriptor | null>(null);
  const identityRevDoneRef = useRef(0);
  // Which tenant ui-prefs were last synced for — boot's own hydrateUIPrefs() already
  // covers the first tenant, so the per-tenant sync effect below must only resync
  // (clear + re-hydrate) the accumulated keys on an ACTUAL tenant change, not on the
  // effect's initial post-boot run.
  const prefsSyncedTenantRef = useRef<string | null>(null);
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
    // Skip the first mount: reloading while on a drawer entry leaves history.state.drawer
    // behind, so the initial closed-state run would fire history.back() and navigate one
    // step away. Only real open/close transitions need syncing.
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

  // Horizontal swipes (show/hide the left pane; rotate running sessions on a phone). The
  // recognition rules and their rationale live in app/swipeGestures.ts; this only wires up
  // the screen-state reads and the side effects.
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    return installSwipeGestures(window, {
      phone: () => mq.matches,
      coarse: coarsePointer,
      // While a modal is up, don't let an edge swipe open the rail behind it.
      modal: () => {
        const { settingsOpen, adminOpen } = useSettingsUI.getState();
        return settingsOpen || adminOpen;
      },
      drawerOpen: () => navOpenRef.current,
      railOpen: () => useLeftRail.getState().open,
      // A popped-out tab is a minimal single-pane UI; switching session is outside what
      // it is for.
      rotatable: () => popoutMode() !== "popout",
      setDrawer: setNavOpen,
      openRailOverlay: () => useLeftRail.getState().openOverlay(),
      closeRail: () => useLeftRail.getState().close(),
      rotateSession: rotateToSession,
    });
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
    const unKeys = wireKeys();
    const unReconcile = wireTerminalReconcile();
    // Attention beacon (docs/log/75 P3): report real interaction while visible, at most
    // once every 60s, so someone who is only reading does not look absent and get their
    // container stopped.
    const unAttention = wireAttentionBeacon();
    const unBrowserReconcile = wireBrowserReconcile();
    const unBrowserAttachmentReconcile = wireBrowserAttachmentReconcile();
    const unWsRefresh = wireWorkspaceRefresh();
    // When a session goes idle, re-read FILES for just that working copy. The listing
    // arrives anyway, so this costs no extra traffic, and firings coalesce per copy.
    const unFilesSessionRefresh = wireFilesSessionRefresh();
    // Unified push channel (traffic reduction P3): register the wiring before connecting
    // so the first snapshot frame is not dropped. The pollers still start as the fallback
    // and skip their tick while pushHealthy.
    const unPushApply = wirePushApply();
    const stopPush = startPushChannel();
    const stopWsPoll = startWorkspacePolling();
    const stopSessPoll = startSessionsPolling();
    const stopReposPoll = startReposPolling();
    // Import jobs (docs/log/78): polls fast only while one is running. The work continues
    // with the browser closed, so this also restores the rows after a reload.
    const stopRepoJobsPoll = startRepoJobsPolling();
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
      unAttention();
      document.removeEventListener("visibilitychange", onPrefsVisible);
      window.removeEventListener("focus", refreshPrefs);
      unHistory();
      unKeys();
      unReconcile();
      unBrowserReconcile();
      unBrowserAttachmentReconcile();
      unWsRefresh();
      unFilesSessionRefresh();
      unPushApply();
      stopPush();
      stopWsPoll();
      stopSessPoll();
      stopReposPoll();
      stopRepoJobsPoll();
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
    // Never leave the previous tenant's owner data in memory for the accumulated ui-prefs
    // keys (working sets and friends): if the new tenant's ui-prefs.json is empty and we
    // treat that as data loss and write it back, the previous tenant's data leaks. Skip
    // this effect's first run after boot, where hydrateUIPrefs() already loaded this
    // tenant's copy.
    if (prefsSyncedTenantRef.current !== null && prefsSyncedTenantRef.current !== tenant) {
      void resyncAccumulatedForIdentitySwitch();
    }
    prefsSyncedTenantRef.current = tenant;
    // pane ids are tab-local, not tenant-global. Never carry an ephemeral Page
    // owned by the previous membership into a same-named pane in the next tenant.
    disposeAllBrowsers();
    disposeAllBrowserAttachments();
    // Tear down the old tenant's push stream before resetting the notification store; the
    // other order lets one of its frames slip in after the reset.
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
  // Resyncing the accumulated ui-prefs keys is the one exception that is also needed here:
  // when only the user changes within the same tenant, the per-tenant sync effect above
  // does not fire, and the switch shows up as a change in identityRev alone.
  useEffect(() => {
    if (!booted || identityRev === identityRevDoneRef.current) return;
    identityRevDoneRef.current = identityRev;
    void resyncAccumulatedForIdentitySwitch();
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

  // With no membership yet (not invited), land on a screen that says so rather than
  // opening the Console (docs/log/61 §61.10.2, P7-2). Invite-only is the default for a new
  // install, so this is the first step, not an edge case; opening the Console would just
  // 403 every request and emit one toast per failure. Placed before the popout branch:
  // with no membership there is no pane to open either.
  if (notProvisioned) return <NotProvisioned />;

  // Minimal pop-out chrome: title bar + the (1-pane) PaneHost, plus the overlay
  // layer (dialogs the reduced command set can still reach + auth/workspace
  // modals). No rail / TopBar / WsBar — expanding (PopoutTitleBar) converts in place.
  if (popout === "popout") {
    return (
      <div className="app-shell popout">
        <PopoutTitleBar />
        <main className="app-main">
          <PaneHost />
        </main>
        {settingsOpen && <SettingsDialog />}
        {adminOpen && <AdminDialog />}
        {tenantOpen && <TenantDialog />}
        {guideOpen && <GuideModal />}
        <SessionModals />
        {/* The launch stack (StartModal / LaunchModal). It renders nothing while there is
            no target, so it does not intrude on the minimal chrome; without it, launching
            from a detached pane (accepting a shared-view handover, docs/log/77, or sending
            a memo) would be a button that does nothing. */}
        <StartHost />
        <WsStartingDialog />
        <AuthExpiredModal />
        <ProviderRequiredModal />
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
          {/* Working sets (docs/log/52): pinned OUTSIDE the scroll area so the active
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
                <WorkItemsSection />
                <MemoQueueSection />
                {schedulerEnabled && <SchedulesSection />}
                <ProjectTree />
                <OtherSessionsSection />
                <SharedSessionsSection />
                <FilesSection />
              </>
            ) : (
              <>
                <StoppedRailSection id="assistant" title={tr("ui.assistant")} icon="comment-discussion" />
                {/* The stopped state is the point (docs/log/80): this reads the CP's cache,
                    which is what makes waking a stopped Workspace from a ticket work. */}
                <WorkItemsSection />
                <MemoQueueSection />
                {schedulerEnabled && <SchedulesSection />}
                <StoppedRailSection id="repos" title={tr("ui.repositories")} icon="repo" />
                <StoppedSessionsSection />
                <SharedSessionsSection />
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
      {tenantOpen && <TenantDialog />}
      {guideOpen && <GuideModal />}
      <StartHost />
      <SessionModals />
      <WsStartingDialog />
      <AuthExpiredModal />
      <ProviderRequiredModal />
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
