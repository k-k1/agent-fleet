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
import { useSettingsUI } from "../store.ts";
import { useSettingsTarget } from "../parts/useSettingsTarget.ts";
import {
  AgyBehavior,
  ClaudeBehavior,
  CodexBehavior,
  CopilotBehavior,
  CursorBehavior,
  KiroBehavior,
  LcppBehavior,
  MuseBehavior,
  OpencodeBehavior,
} from "./AgentBehaviors.tsx";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useT } from "../../../lib/i18n/index.ts";

// AgentsTab is Settings > Connections > Agents: one card per harness with what it does (ADR 0138
// D16), never what it signs in with — sign-ins, keys and endpoints are on Model credentials
// (CredentialsTab). Each card's behaviour settings are the client-side launch defaults, which
// render in every workspace state, plus the container-backed toggles (Remote Control,
// notifications, RTK, nudge), which wait for the workspace. The rtk-effect analytics live in the
// usage tab — monitoring is not a setting. What applies to every session has its own pages:
// Settings > Sessions and Settings > Image generation.
export function AgentsTab() {
  const tr = useT();
  const toast = useToast();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // The behaviour toggles go through the in-container Agent (proxyAgentREST → 502 when stopped),
  // so they wait for a running workspace; the client launch defaults do not.
  const running = wsState === "running";
  // Behavior settings, loaded independently so a missing/old endpoint degrades in
  // place (hides that card's toggles) instead of blanking the page. claude:
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
    loadSettings();
  }, [running, loadSettings]);

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

  // A deep link naming a harness lands on its card.
  const rootRef = useRef<HTMLDivElement>(null);
  useSettingsTarget(rootRef, true);

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
      {/* Sign-ins, keys and endpoints are on Model credentials (ADR 0138 D16). */}
      <p className="muted ds-note">
        {tr("agents.credentials_moved")}{" "}
        <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("credentials")}>
          {tr("set.tab_credentials")}
        </button>
      </p>
      {running && <p className="muted ds-note">{tr("agents.note_apply")}</p>}
      <ClaudeBehavior claude={claude} updateClaude={updateClaude} />
      <CodexBehavior codex={codex} updateCodex={updateCodex} agents={agents} updateAgents={updateAgents} />
      <CursorBehavior />
      <CopilotBehavior agents={agents} updateAgents={updateAgents} />
      <KiroBehavior />
      <AgyBehavior agents={agents} updateAgents={updateAgents} />
      <OpencodeBehavior agents={agents} updateAgents={updateAgents} />
      <LcppBehavior />
      <MuseBehavior />
      {running && agents === false && <p className="muted ds-note">{tr("agents.rtk_unsupported")}</p>}
    </div>
  );
}
