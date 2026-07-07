// App shell for the next console — P0 skeleton (docs/22).
//
// Renders the real chrome frame (top bar / WS bar / left rail / main) wired to
// the new zustand stores against the live backend, plus a temporary ui/
// primitives showcase in the main area so tokens + primitives can be eyeballed
// in both themes. The showcase goes away as real features land (P1+).
import { useEffect, useState } from "react";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, wsBusy, startWorkspacePolling } from "../core/store/workspace.ts";
import { hydrateUIPrefs, useSettings, setSetting } from "../lib/settings.ts";
import { Button, IconButton } from "../ui/Button.tsx";
import { Pill } from "../ui/Pill.tsx";
import type { PillTone } from "../ui/Pill.tsx";
import { EmptyState } from "../ui/EmptyState.tsx";

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

// Workspace-state chip tone, mirroring the old bar's reading of the CP state.
function wsTone(state: string): PillTone {
  if (state === "running") return "ok";
  if (state === "stopped") return "muted";
  if (state === "unknown" || state === "…") return "muted";
  return "warn"; // transitions ("starting…"/"stopping…") and anything unexpected
}

function WsBar() {
  const ws = useWorkspaceStore();
  const busy = wsBusy(ws.state);
  const running = ws.state === "running";
  // Stop kills every session in the container — a real footgun from a skeleton
  // page, so require a second click (the ConfirmDialog port lands with P2 modals).
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
    </div>
  );
}

// Temporary P0 showcase — lets the user eyeball tokens + primitives in both
// themes against the real backdrop. Removed once real views land.
function Showcase() {
  return (
    <div className="app-showcase">
      <section>
        <h2>Button</h2>
        <div className="app-row">
          <Button icon="add">既定</Button>
          <Button variant="primary" icon="rocket">
            プライマリ
          </Button>
          <Button variant="ghost" icon="gear">
            ゴースト
          </Button>
          <Button variant="danger" icon="trash">
            危険
          </Button>
          <Button disabled>無効</Button>
          <Button small icon="git-branch">
            小
          </Button>
          <IconButton icon="split-horizontal" label="分割" />
        </div>
      </section>
      <section>
        <h2>Pill</h2>
        <div className="app-row">
          <Pill tone="ok">running</Pill>
          <Pill tone="warn">starting…</Pill>
          <Pill tone="danger">error</Pill>
          <Pill tone="accent">3 変更</Pill>
          <Pill>stopped</Pill>
        </div>
      </section>
      <section>
        <h2>EmptyState</h2>
        <div className="app-empty-demo">
          <EmptyState icon="inbox" title="まだ何もありません" hint="P1 でターミナル + レイアウトコアが移植されます。">
            <Button variant="primary" small icon="add">
              新規セッション
            </Button>
          </EmptyState>
        </div>
      </section>
    </div>
  );
}

export function App() {
  // Boot: resolve tenant → pull per-user display prefs → workspace state + poll.
  useEffect(() => {
    void (async () => {
      await useTenantStore.getState().init();
      void hydrateUIPrefs();
      void useWorkspaceStore.getState().refresh();
    })();
    return startWorkspacePolling();
  }, []);
  return (
    <div className="app-shell">
      <TopBar />
      <WsBar />
      <div className="app-body">
        <nav className="app-rail">
          <EmptyState icon="layout-sidebar-left" title="左レール" hint="Sessions / Repos / Files は P2 で移植。" />
        </nav>
        <main className="app-main">
          <Showcase />
        </main>
      </div>
    </div>
  );
}
