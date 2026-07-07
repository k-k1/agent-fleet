// App shell for the next console — P1: terminal + layout core (docs/22).
//
// The main area is the real PaneHost (split panes + live xterm PTYs) wired to
// the zustand stores; the rail carries a provisional sessions list (full
// SessionsSection lands in P2). Boot order: resolve tenant → per-tenant layout
// restore (with old-format migration) → per-user display prefs → workspace +
// sessions polling. History (back/forward) traverses layout states.
import { useEffect, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, wsBusy, startWorkspacePolling } from "../core/store/workspace.ts";
import { useLayoutStore, wireLayoutHistory } from "../layout/store.ts";
import { wireTerminalReconcile } from "../terminal/service.ts";
import { useSessionsStore, startSessionsPolling } from "../features/sessions/store.ts";
import { hydrateUIPrefs, useSettings, setSetting } from "../lib/settings.ts";
import { PaneHost } from "../features/panes/PaneHost.tsx";
import { SessionsRail } from "../features/sessions/SessionsRail.tsx";
import { Button, IconButton } from "../ui/Button.tsx";
import { Pill } from "../ui/Pill.tsx";
import type { PillTone } from "../ui/Pill.tsx";

function TopBar() {
  const whoami = useTenantStore((s) => s.whoami);
  const tenants = useTenantStore((s) => s.tenants);
  const tenant = useTenantStore((s) => s.tenant);
  const showPicker = useTenantStore((s) => s.showPicker);
  const select = useTenantStore((s) => s.select);
  const settings = useSettings();
  return (
    <header className="app-topbar">
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
  const [booted, setBooted] = useState(false);

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
    <div className="app-shell">
      <TopBar />
      <WsBar />
      <div className="app-body">
        <nav className="app-rail">
          <SessionsRail />
        </nav>
        <main className="app-main">
          <PaneHost />
        </main>
      </div>
    </div>
  );
}
