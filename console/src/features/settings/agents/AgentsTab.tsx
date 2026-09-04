import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { api, apiJSON } from "../../../core/api/client.ts";
import { Button } from "../../../ui/Button.tsx";
import { OnOff, Row } from "../parts/controls.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { useConnections } from "../parts/useConnections.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { ClaudeCard } from "./ClaudeCard.tsx";
import { CodexCard } from "./CodexCard.tsx";
import { CursorCard } from "./CursorCard.tsx";
import { CopilotCard } from "./CopilotCard.tsx";
import { KiroCard } from "./KiroCard.tsx";
import { AgyCard } from "./AgyCard.tsx";
import { OpencodeCard } from "./OpencodeCard.tsx";

// AgentsTab is the per-agent home. Each card is split into two levels so the two
// concerns read as a hierarchy rather than one flat block:
//   1. CONNECTION (top) — the auth flow + status. Needs the workspace running (secrets
//      are stored container-side via the Agent; the REST proxy 502s while stopped).
//   2. BEHAVIOR SETTINGS (a collapsed disclosure, below) — the per-agent behavior:
//      client-side launch defaults (model / effort / start-mode, in the local settings
//      store) plus the container-backed toggles (Remote Control / notifications / RTK /
//      nudge). Launch defaults are client-only, so the cards render even while stopped —
//      you can set a default model before starting; only the connection + runtime toggles
//      wait for the workspace. Git-hosting agents live in GitTab; the rtk-effect analytics
//      that used to sit here lives in the usage tab (features/usage) — monitoring is not a
//      setting.
export function AgentsTab() {
  const tr = useT();
  const toast = useToast();
  // Client-side session pref (automatic title suggestions) — persisted in the local settings
  // store, so it shows regardless of workspace state (unlike the container-backed
  // toggles, which need the Agent/CLI). Default models live in each card's behavior settings.
  const s = useSettings();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // Connection auth AND the behavior toggles both go through the in-container Agent
  // (proxyAgentREST → 502 when stopped), so those wait for a running workspace. The
  // client launch defaults do not (see CardSettings, rendered in every state).
  const running = wsState === "running";
  // Shared connection loader (also used by GitTab); reload() refetches + bumps global
  // listeners on connect/disconnect.
  const { conns, reload } = useConnections();
  // Behavior settings, loaded independently so a missing/old endpoint degrades in
  // place (hides that card's toggles) instead of blanking the connect UI. claude:
  // null = loading/unavailable, object = ready. agents: null = loading, false =
  // endpoint missing (older image), object = ready.
  const [claude, setClaude] = useState<any>(null);
  const [codex, setCodex] = useState<any>(null);
  const [agents, setAgents] = useState<any>(null);

  const loadSettings = useCallback(() => {
    api("api/claude/settings")
      .then((c) => setClaude(c && !c.error ? c : null))
      .catch(() => setClaude(null));
    api("api/codex/settings")
      .then((c) => setCodex(c && !c.error ? c : null))
      .catch(() => setCodex(null));
    api("api/agents/rtk")
      .then((a) => setAgents(a && !a.error ? a : false))
      .catch(() => setAgents(false));
  }, []);

  // (Re)load when the workspace is running — including when it transitions
  // stopped→running while this dialog is open, so settings appear without a reopen.
  useEffect(() => {
    if (!running) return;
    reload();
    loadSettings();
  }, [running, reload, loadSettings]);

  // One save handler per settings endpoint — identical error contract, differing
  // only in path + setter.
  const mkUpdate =
    (path: string, setState: (d: any) => void) => async (patch: unknown) => {
      const d = await apiJSON(path, "PUT", patch);
      if (d && d.error) {
        toast(tr("common.save_failed_msg", { msg: d.error.message || "" }));
        return;
      }
      setState(d);
    };
  const updateClaude = mkUpdate("api/claude/settings", setClaude);
  const updateCodex = mkUpdate("api/codex/settings", setCodex);
  const updateAgents = mkUpdate("api/agents/rtk", setAgents);

  // Session prefs render in every state (stopped / loading / running) since they're
  // local, not container-backed.
  const sessionSettings = (
    <section className="ds-group">
      <h4 className="ds-title">{tr("agents.session")}</h4>
      {/* Automatic title suggestions (autoTitleSuggest) moved to Settings > AI assist
          (docs/log/84). Here it looked like a session setting, but the one key also disabled the
          AI branch-name suggestion; each AI-generation on/off now has a single home. */}
      {/* Cross-session messaging (docs/log/58 / ADR 0041) sits here rather than inside a card
          because it applies to all 7 kinds that af's own MCP is distributed to; it is not any
          one agent's setting (in the claude card it would look claude-only). */}
      <Row label={tr("agents.peer_messaging")}>
        <OnOff value={s.peerMessaging} onChange={(v) => setSetting("peerMessaging", v)} />
      </Row>
      <p className="muted ds-note">{tr("agents.note_peer_messaging")}</p>
    </section>
  );

  // While running but the connection snapshot hasn't loaded yet, hold the cards back a
  // beat (avoids a flash of "not connected" idle flows). Stopped renders the cards immediately
  // (degraded): their launch defaults are reachable, connection waits for start.
  const loading = running && !conns;

  return (
    <div className="conns">
      {sessionSettings}
      {!running && (
        <div className="agents-ws-hint">
          <p className="muted ds-note">{tr("agents.ws_required_hint")}</p>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </div>
      )}
      {loading ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          {running && <p className="muted ds-note">{tr("agents.note_apply")}</p>}
          <ClaudeCard running={running} st={conns?.claude} reload={reload} claude={claude} updateClaude={updateClaude} />
          <CodexCard
            running={running}
            st={conns?.codex}
            reload={reload}
            codex={codex}
            updateCodex={updateCodex}
            agents={agents}
            updateAgents={updateAgents}
          />
          <CursorCard running={running} st={conns?.cursor} reload={reload} />
          <CopilotCard running={running} st={conns?.copilot} agents={agents} updateAgents={updateAgents} />
          <KiroCard running={running} st={conns?.kiro} reload={reload} />
          <AgyCard running={running} st={conns?.agy} reload={reload} agents={agents} updateAgents={updateAgents} />
          <OpencodeCard
            running={running}
            st={conns?.opencode}
            reload={reload}
            agents={agents}
            updateAgents={updateAgents}
          />
          {running && agents === false && <p className="ps-note">{tr("agents.rtk_unsupported")}</p>}
        </>
      )}
    </div>
  );
}
