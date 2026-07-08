// App shell for the next console — P1: terminal + layout core (docs/22).
//
// The main area is the real PaneHost (split panes + live xterm PTYs) wired to
// the zustand stores; the rail carries a provisional sessions list (full
// SessionsSection lands in P2). Boot order: resolve tenant → per-tenant layout
// restore (with old-format migration) → per-user display prefs → workspace +
// sessions polling. History (back/forward) traverses layout states.
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, startWorkspacePolling } from "../core/store/workspace.ts";
import { useLayoutStore, wireLayoutHistory } from "../layout/store.ts";
import { wireTerminalReconcile } from "../terminal/service.ts";
import { useSessionsStore, startSessionsPolling } from "../features/sessions/store.ts";
import { SessionModals } from "../features/sessions/SessionModals.tsx";
import { useSessionNotifications } from "../features/sessions/useSessionNotifications.ts";
import { useReposStore } from "../features/repos/store.ts";
import { useFilesStore } from "../features/files/store.ts";
import { useChatStore } from "../features/chat/store.ts";
import { hydrateUIPrefs } from "../lib/settings.ts";
import { MOBILE_QUERY } from "../lib/device.ts";
import { PaneHost } from "../features/panes/PaneHost.tsx";
import { LayoutMap } from "../features/panes/LayoutMap.tsx";
import { AssistantSection } from "../features/chat/AssistantSection.tsx";
import { MemoQueueSection } from "../features/memo/MemoQueueSection.tsx";
import { ProjectTree } from "../features/project/ProjectTree.tsx";
import { OtherSessionsSection } from "../features/project/OtherSessionsSection.tsx";
import { WsBar } from "./WsBar.tsx";
import { TopBar } from "./TopBar.tsx";
import { useSettingsUI, wireSettingsHistory } from "../features/settings/store.ts";
import { SettingsDialog } from "../features/settings/SettingsDialog.tsx";
import { AdminDialog } from "../features/settings/AdminDialog.tsx";

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
  const tenant = useTenantStore((s) => s.tenant);
  const layout = useLayoutStore((s) => s.layout);
  const settingsOpen = useSettingsUI((s) => s.settingsOpen);
  const adminOpen = useSettingsUI((s) => s.adminOpen);
  const [booted, setBooted] = useState(false);

  // Left rail visibility. Desktop: leftOpen (persisted) + leftMode "push" (docks,
  // main reflows) / "overlay" (floats above main). Mobile (≤760px): the rail is an
  // off-canvas drawer driven by navOpen; selecting anything closes it.
  const [navOpen, setNavOpen] = useState(false);
  const navOpenRef = useRef(navOpen);
  navOpenRef.current = navOpen;
  const [leftOpen, setLeftOpen] = useState<boolean>(() => localStorage.getItem("af-left-open") !== "0");
  const [leftMode, setLeftMode] = useState<string>(() =>
    localStorage.getItem("af-left-mode") === "overlay" ? "overlay" : "push",
  );
  const toggleNav = () => setNavOpen((o) => !o);
  // Desktop-only (TopBar routes mobile taps to toggleNav itself).
  const toggleLeft = () =>
    setLeftOpen((o) => {
      const n = !o;
      localStorage.setItem("af-left-open", n ? "1" : "0");
      return n;
    });
  const closeLeft = () => {
    setLeftOpen(false);
    localStorage.setItem("af-left-open", "0");
  };
  const toggleLeftMode = () =>
    setLeftMode((m) => {
      const n = m === "push" ? "overlay" : "push";
      localStorage.setItem("af-left-mode", n);
      return n;
    });

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
  useEffect(() => {
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

  // Mobile: swipe right (from the left third) opens the drawer; swipe left
  // closes it. Passive listeners; vertical drags are left for scrolling.
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    const DIST = 50;
    let sx = 0,
      sy = 0,
      mode: "open" | "close" | null = null;
    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      mode = null;
      // While a modal is up, don't let an edge swipe open the drawer behind it (the
      // settings modal also uses horizontal swipes for its own tabs).
      const { settingsOpen, adminOpen } = useSettingsUI.getState();
      if (t && mq.matches && !settingsOpen && !adminOpen) {
        if (navOpenRef.current) mode = "close";
        else if (t.clientX < Math.min(window.innerWidth * 0.33, 160)) mode = "open";
      }
      if (t) {
        sx = t.clientX;
        sy = t.clientY;
      }
    };
    const onMove = (e: TouchEvent) => {
      if (!mode) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - sx;
      const dy = t.clientY - sy;
      if (Math.abs(dx) <= Math.abs(dy)) return;
      if (mode === "open" && dx > DIST) {
        setNavOpen(true);
        mode = null;
      } else if (mode === "close" && dx < -DIST) {
        setNavOpen(false);
        mode = null;
      }
    };
    const onEnd = () => {
      mode = null;
    };
    window.addEventListener("touchstart", onStart, { passive: true });
    window.addEventListener("touchmove", onMove, { passive: true });
    window.addEventListener("touchend", onEnd, { passive: true });
    window.addEventListener("touchcancel", onEnd, { passive: true });
    return () => {
      window.removeEventListener("touchstart", onStart);
      window.removeEventListener("touchmove", onMove);
      window.removeEventListener("touchend", onEnd);
      window.removeEventListener("touchcancel", onEnd);
    };
  }, []);

  // One-time wiring: history (back/forward → layout), terminal reconciliation,
  // pollers. All return cleanups, so StrictMode's double-invoke is safe.
  useEffect(() => {
    const unHistory = wireLayoutHistory();
    const unModalHistory = wireSettingsHistory();
    const unReconcile = wireTerminalReconcile();
    const unWsRefresh = wireWorkspaceRefresh();
    const stopWsPoll = startWorkspacePolling();
    const stopSessPoll = startSessionsPolling();
    void (async () => {
      await useTenantStore.getState().init();
      void hydrateUIPrefs();
      setBooted(true);
    })();
    return () => {
      unHistory();
      unModalHistory();
      unReconcile();
      unWsRefresh();
      stopWsPoll();
      stopSessPoll();
    };
  }, []);

  // Per-tenant sync: on boot completion AND on tenant switch — restore that
  // tenant's saved split (migrating the old console's format on first load)
  // and refetch tenant-scoped data.
  useEffect(() => {
    if (!booted) return;
    useLayoutStore.getState().load(tenant);
    void useWorkspaceStore.getState().refresh();
    void useSessionsStore.getState().refresh();
  }, [booted, tenant]);

  // openNewSession signal (WS bar 新規 / onboarding): the dialog mounts inside the
  // left rail — on mobile that's an off-canvas drawer whose CSS transform would
  // offset the modal's fixed positioning, so raise the drawer first (no-op on
  // desktop, where the rail is in flow).
  const newSessionTick = useSessionsStore((s) => s.newSessionTick);
  useEffect(() => {
    if (newSessionTick > 0 && window.matchMedia(MOBILE_QUERY).matches) setNavOpen(true);
  }, [newSessionTick]);

  // Desktop notifications on claude state arrivals — lives at the shell now that
  // the flat Sessions section no longer owns the rail.
  useSessionNotifications();

  return (
    <div
      className={
        "app-shell" +
        (leftOpen ? "" : " left-collapsed") +
        (leftMode === "overlay" ? " left-overlay" : "") +
        (navOpen ? " nav-open" : "")
      }
    >
      <TopBar toggleNav={toggleNav} toggleLeft={toggleLeft} toggleLeftMode={toggleLeftMode} />
      <WsBar />
      <div className="app-body">
        <nav className="app-rail">
          <LayoutMap />
          {/* Project-first IA: Assistant + Memo pinned on top (global tools), then
              the working-copy tree (each node nests its sessions + files), then the
              repo-less session catch-all. Files are per-node now (ProjectFiles),
              so there's no global Files section. */}
          <div className="app-rail-scroll">
            <AssistantSection />
            <MemoQueueSection />
            <ProjectTree />
            <OtherSessionsSection />
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
      <SessionModals />
    </div>
  );
}
