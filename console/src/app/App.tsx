// App shell for the next console — P1: terminal + layout core (docs/22).
//
// The main area is the real PaneHost (split panes + live xterm PTYs) wired to
// the zustand stores; the rail carries a provisional sessions list (full
// SessionsSection lands in P2). Boot order: resolve tenant → per-tenant layout
// restore (with old-format migration) → per-user display prefs → workspace +
// sessions polling. History (back/forward) traverses layout states.
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, wsBusy, startWorkspacePolling } from "../core/store/workspace.ts";
import { useLayoutStore, wireLayoutHistory } from "../layout/store.ts";
import { wireTerminalReconcile } from "../terminal/service.ts";
import { useSessionsStore, startSessionsPolling } from "../features/sessions/store.ts";
import { hydrateUIPrefs, useSettings, setSetting } from "../lib/settings.ts";
import { MOBILE_QUERY } from "../lib/device.ts";
import { PaneHost } from "../features/panes/PaneHost.tsx";
import { LayoutMap } from "../features/panes/LayoutMap.tsx";
import { SessionsSection } from "../features/sessions/SessionsSection.tsx";
import { ReposSection } from "../features/repos/ReposSection.tsx";
import { FilesSection } from "../features/files/FilesSection.tsx";
import { Button, IconButton } from "../ui/Button.tsx";
import { Pill } from "../ui/Pill.tsx";
import type { PillTone } from "../ui/Pill.tsx";

interface TopBarProps {
  onToggleLeft: () => void;
  onToggleLeftMode: () => void;
}

function TopBar({ onToggleLeft, onToggleLeftMode }: TopBarProps) {
  const whoami = useTenantStore((s) => s.whoami);
  const tenants = useTenantStore((s) => s.tenants);
  const tenant = useTenantStore((s) => s.tenant);
  const showPicker = useTenantStore((s) => s.showPicker);
  const select = useTenantStore((s) => s.select);
  const settings = useSettings();
  return (
    <header className="app-topbar">
      <IconButton
        icon="menu"
        label="左ペインを開閉（ダブルクリックで固定⇄オーバーレイ切替）"
        onClick={onToggleLeft}
        onDoubleClick={onToggleLeftMode}
      />
      <span className="app-brand">Agent Fleet</span>
      <Pill tone="accent">next</Pill>
      {showPicker && (
        <select
          className="app-tenant"
          value={tenant}
          onChange={(e) => select(e.target.value)}
          aria-label="テナント"
        >
          {tenants.map((t) => (
            <option key={t.slug} value={t.slug}>
              {t.name || t.slug}
            </option>
          ))}
        </select>
      )}
      <span className="app-spacer" />
      <span className="app-whoami">{whoami?.email || whoami?.user || ""}</span>
      <IconButton
        icon={settings.theme === "light" ? "color-mode" : "lightbulb"}
        label={settings.theme === "light" ? "ダークテーマへ" : "ライトテーマへ"}
        onClick={() => setSetting("theme", settings.theme === "light" ? "dark" : "light")}
      />
    </header>
  );
}

function wsTone(state: string): PillTone {
  if (state === "running") return "ok";
  if (state === "stopped" || state === "unknown" || state === "…") return "muted";
  return "warn";
}

function WsBar() {
  const ws = useWorkspaceStore();
  const busy = wsBusy(ws.state);
  const running = ws.state === "running";
  const layout = useLayoutStore((s) => s.layout);
  const splitRight = useLayoutStore((s) => s.splitRight);
  const splitDown = useLayoutStore((s) => s.splitDown);
  // Stop kills every session in the container — require a second click until the
  // ConfirmDialog port lands (P2 modals).
  const [confirmStop, setConfirmStop] = useState(false);
  useEffect(() => {
    if (!confirmStop) return;
    const t = setTimeout(() => setConfirmStop(false), 4000);
    return () => clearTimeout(t);
  }, [confirmStop]);
  return (
    <div className="app-wsbar">
      <Pill tone={wsTone(ws.state)} icon="vm">
        {ws.state}
      </Pill>
      {running ? (
        <Button
          small
          variant={confirmStop ? "danger" : "default"}
          icon="debug-stop"
          disabled={busy}
          onClick={() => {
            if (!confirmStop) return setConfirmStop(true);
            setConfirmStop(false);
            void ws.stop();
          }}
        >
          {confirmStop ? "もう一度クリックで停止" : "停止"}
        </Button>
      ) : (
        <Button small variant="primary" icon="play" disabled={busy} onClick={() => void ws.start()}>
          起動
        </Button>
      )}
      <span className="app-spacer" />
      <IconButton
        icon="split-horizontal"
        label="右に分割"
        onClick={() => splitRight()}
      />
      <IconButton
        icon="split-vertical"
        label="上下に分割"
        onClick={() => splitDown(layout.activeId)}
      />
    </div>
  );
}

export function App() {
  const tenant = useTenantStore((s) => s.tenant);
  const layout = useLayoutStore((s) => s.layout);
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
  const toggleLeft = () => {
    if (window.matchMedia(MOBILE_QUERY).matches) {
      setNavOpen((o) => !o);
      return;
    }
    setLeftOpen((o) => {
      const n = !o;
      localStorage.setItem("af-left-open", n ? "1" : "0");
      return n;
    });
  };
  const toggleLeftMode = () =>
    setLeftMode((m) => {
      const n = m === "push" ? "overlay" : "push";
      localStorage.setItem("af-left-mode", n);
      return n;
    });

  // Any navigation (layout change) closes the mobile drawer so the main area
  // comes forward — covers every open-path without threading closeNav around.
  useEffect(() => {
    setNavOpen(false);
  }, [layout]);

  // Mobile: swipe right (from the left third) opens the drawer; swipe left
  // closes it. Passive listeners; vertical drags are left for scrolling.
  // TODO(P8): drawer history entry (back button reopens the drawer).
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    const DIST = 50;
    let sx = 0,
      sy = 0,
      mode: "open" | "close" | null = null;
    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      mode = null;
      if (t && mq.matches) {
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
    const unReconcile = wireTerminalReconcile();
    const stopWsPoll = startWorkspacePolling();
    const stopSessPoll = startSessionsPolling();
    void (async () => {
      await useTenantStore.getState().init();
      void hydrateUIPrefs();
      setBooted(true);
    })();
    return () => {
      unHistory();
      unReconcile();
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

  return (
    <div
      className={
        "app-shell" +
        (leftOpen ? "" : " left-collapsed") +
        (leftMode === "overlay" ? " left-overlay" : "") +
        (navOpen ? " nav-open" : "")
      }
    >
      <TopBar onToggleLeft={toggleLeft} onToggleLeftMode={toggleLeftMode} />
      <WsBar />
      <div className="app-body">
        <nav className="app-rail">
          <LayoutMap />
          <div className="app-rail-scroll">
            <SessionsSection />
            <ReposSection />
            <FilesSection />
          </div>
        </nav>
        {/* Mobile drawer backdrop: tap outside to close. */}
        <div className="app-nav-backdrop" onClick={() => setNavOpen(false)} />
        <main className="app-main">
          <PaneHost />
        </main>
      </div>
    </div>
  );
}
