import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
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
import { OwnSettingsLine } from "../credentials/MemberLines.tsx";
import { useMemberCatalog } from "../credentials/memberCatalog.ts";
import { HarnessHead } from "./HarnessHead.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { useModelRouteProfiles } from "../../repos/bedrockProfiles.ts";
import { waitPrefsSaved } from "../../../lib/settings.ts";
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

  // What may be shown (ADR 0138 D10a): a harness the policy forbids, or the member switched off,
  // has no card here. The catalogue is asked afresh; with no answer (stopped, out, failed) no
  // card is drawn, rather than every card.
  const catalog = useMemberCatalog(running);
  // What every harness card opens with: its on/off, default route and "Runs on" (HarnessHead).
  // The default route's connections are the member's: marked AWS profiles, and the llama.cpp
  // endpoint's binding (GET /connections), the reference the Agent holds a launch to.
  const identity = useTenantStore((st) => `${st.tenant}|${st.identityRev}`);
  const { conns, reload: reloadConns } = useConnections();
  useEffect(() => {
    if (running) reloadConns();
  }, [running, reloadConns, identity]);
  const anyBedrock = [...(catalog.kinds?.values() ?? [])].some((k) => [...(k.routes ?? []), ...(k.fixedRoutes ?? [])].includes("bedrock"));
  const profiles = useModelRouteProfiles(running && anyBedrock, identity);
  const lcppBinding = (conns?.lcpp as { binding?: string } | undefined)?.binding;
  const head = (kind: string, ownSwitch = false) => (
    <HarnessHead
      kind={kind}
      live={catalog.kinds?.get(kind)}
      profiles={profiles}
      lcppBinding={lcppBinding}
      ownSwitch={ownSwitch}
      onSwitched={() => void waitPrefsSaved().then(() => catalog.refresh())}
    />
  );
  const behaviors: [string, ReactNode][] = [
    ["claude", <ClaudeBehavior key="claude" head={head("claude")} claude={claude} updateClaude={updateClaude} />],
    ["codex", <CodexBehavior key="codex" head={head("codex")} codex={codex} updateCodex={updateCodex} agents={agents} updateAgents={updateAgents} />],
    ["cursor", <CursorBehavior key="cursor" head={head("cursor")} />],
    ["copilot", <CopilotBehavior key="copilot" head={head("copilot")} agents={agents} updateAgents={updateAgents} />],
    ["kiro", <KiroBehavior key="kiro" head={head("kiro")} />],
    ["agy", <AgyBehavior key="agy" head={head("agy")} agents={agents} updateAgents={updateAgents} />],
    ["opencode", <OpencodeBehavior key="opencode" head={head("opencode")} agents={agents} updateAgents={updateAgents} />],
    // llama.cpp keeps its own on/off (lcppEnabled), drawn by its card.
    ["lcpp", <LcppBehavior key="lcpp" head={head("lcpp", true)} />],
    ["muse", <MuseBehavior key="muse" head={head("muse")} />],
  ];

  // A deep link naming a harness lands on its card once the catalogue has drawn the cards.
  const rootRef = useRef<HTMLDivElement>(null);
  useSettingsTarget(rootRef, catalog.kinds !== null || !running);

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
      <OwnSettingsLine hidden={running && catalog.hiddenByOwn} onReset={catalog.refresh} />
      {/* Sign-ins, keys and endpoints are on Model credentials (ADR 0138 D16). */}
      <p className="muted ds-note">
        {tr("agents.credentials_moved")}{" "}
        <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("credentials")}>
          {tr("set.tab_credentials")}
        </button>
      </p>
      {running && catalog.kinds === null && <p className="muted ds-note">{tr("cred.checking")}</p>}
      {running && catalog.kinds !== null && <p className="muted ds-note">{tr("agents.note_apply")}</p>}
      {catalog.kinds && behaviors.filter(([k]) => catalog.kinds!.has(k)).map(([, node]) => node)}
      {running && agents === false && <p className="muted ds-note">{tr("agents.rtk_unsupported")}</p>}
    </div>
  );
}
