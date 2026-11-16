import { ConnChecking, type ConnPhase } from "../parts/providerCard.tsx";
import { useCallback, useEffect, useRef, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { errDetail } from "../../../core/api/client.ts";
import {
  getAgentRtk,
  getClaudeSettings,
  getCodexSettings,
  isOk,
  putAgentRtk,
  putClaudeSettings,
  putCodexSettings,
  type AgentRtkSettings,
  type ClaudeSettings,
  type CodexSettings,
  type Reply,
} from "../api.ts";
import { Button } from "../../../ui/Button.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { useSettingsTarget } from "../parts/useSettingsTarget.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { ClaudeCard } from "./ClaudeCard.tsx";
import { CodexCard } from "./CodexCard.tsx";
import { CursorCard } from "./CursorCard.tsx";
import { CopilotCard } from "./CopilotCard.tsx";
import { KiroCard } from "./KiroCard.tsx";
import { AgyCard } from "./AgyCard.tsx";
import { OpencodeCard } from "./OpencodeCard.tsx";
import { LcppCard } from "./LcppCard.tsx";
import { MuseCard } from "./MuseCard.tsx";

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
//      lives in the usage tab (features/usage) — monitoring is not a setting. What applies to
//      every session whatever its agent has its own pages (ADR 0138 D16): Settings > Sessions
//      (SessionsTab) and Settings > Image generation (ImagegenTab).
export function AgentsTab() {
  const tr = useT();
  const toast = useToast();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // Connection auth AND the behavior toggles both go through the in-container Agent
  // (proxyAgentREST → 502 when stopped), so those wait for a running workspace. The
  // client launch defaults do not (see CardSettings, rendered in every state).
  const running = wsState === "running";
  // Shared connection loader (also used by GitTab); reload() refetches + bumps global
  // listeners on connect/disconnect.
  const { conns, reload, failed } = useConnections();
  // Behavior settings, loaded independently so a missing/old endpoint degrades in
  // place (hides that card's toggles) instead of blanking the connect UI. claude:
  // null = loading/unavailable, object = ready. agents: null = loading, false =
  // endpoint missing (older image), object = ready.
  const [claude, setClaude] = useState<ClaudeSettings | null>(null);
  const [codex, setCodex] = useState<CodexSettings | null>(null);
  const [agents, setAgents] = useState<AgentRtkSettings | false | null>(null);

  const loadSettings = useCallback(() => {
    getClaudeSettings()
      .then((c) => setClaude(isOk(c) ? c : null))
      .catch(() => setClaude(null));
    getCodexSettings()
      .then((c) => setCodex(isOk(c) ? c : null))
      .catch(() => setCodex(null));
    getAgentRtk()
      .then((a) => setAgents(isOk(a) ? a : false))
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
    <P, T>(put: (patch: P) => Promise<Reply<T>>, setState: (d: T) => void) =>
    async (patch: P) => {
      const d = await put(patch);
      if (!isOk(d)) {
        toast(tr("common.save_failed_msg", { msg: errDetail(d?.error) }));
        return;
      }
      setState(d);
    };
  const updateClaude = mkUpdate(putClaudeSettings, setClaude);
  const updateCodex = mkUpdate(putCodexSettings, setCodex);
  const updateAgents = mkUpdate(putAgentRtk, setAgents);

  // Running but the connection snapshot has not arrived: the cards draw at once with a
  // "checking…" chip (ConnChecking) rather than hiding the list or claiming "not connected".
  // They get running=false meanwhile, which is what keeps their login flows from flashing.
  const checking = running && !conns;
  const connFailed = running && !!conns && failed;
  const phase: ConnPhase = checking ? "checking" : connFailed ? "failed" : "ok";
  const cardsRunning = running && phase === "ok";

  // A deep-link from elsewhere (the transcript's Re-authenticate button) names the agent card to
  // land on; scrolling waits for the snapshot because
  // the login fields it brings in change the cards' heights and would push a late target off screen.
  const rootRef = useRef<HTMLDivElement>(null);
  useSettingsTarget(rootRef, phase !== "checking");

  return (
    <div className="conns" ref={rootRef}>
      {!running && (
        <div className="agents-ws-hint">
          <p className="muted ds-note">{tr("agents.ws_required_hint")}</p>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </div>
      )}
      <ConnChecking.Provider value={phase}>
        {connFailed && (
          <div className="agents-ws-hint">
            <p className="muted ds-note">{tr("conn.check_failed_note")}</p>
            <Button icon="refresh" onClick={reload}>
              {tr("conn.retry")}
            </Button>
          </div>
        )}
          {running && <p className="muted ds-note">{tr("agents.note_apply")}</p>}
          <ClaudeCard running={cardsRunning} st={conns?.claude} reload={reload} claude={claude} updateClaude={updateClaude} />
          <CodexCard
            running={cardsRunning}
            st={conns?.codex}
            reload={reload}
            codex={codex}
            updateCodex={updateCodex}
            agents={agents}
            updateAgents={updateAgents}
          />
          <CursorCard running={cardsRunning} st={conns?.cursor} reload={reload} />
          <CopilotCard running={cardsRunning} st={conns?.copilot} agents={agents} updateAgents={updateAgents} />
          <KiroCard running={cardsRunning} st={conns?.kiro} reload={reload} />
          <AgyCard running={cardsRunning} st={conns?.agy} reload={reload} agents={agents} updateAgents={updateAgents} />
          <OpencodeCard
            running={cardsRunning}
            st={conns?.opencode}
            reload={reload}
            agents={agents}
            updateAgents={updateAgents}
          />
          <LcppCard running={cardsRunning} st={conns?.lcpp} reload={reload} />
          <MuseCard running={cardsRunning} st={conns?.muse} reload={reload} />
          {running && agents === false && <p className="muted ds-note">{tr("agents.rtk_unsupported")}</p>}
      </ConnChecking.Provider>
    </div>
  );
}
