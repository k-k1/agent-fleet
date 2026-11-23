// OnboardingCard — the first-run guide: the two steps everyone needs (start the workspace,
// connect an agent), then a goal choice: chat (repo-less Q&A / translation, one click) or
// development (git → clone → first session, expanded on demand).
//
// SetupCard is that checklist as the Setup section at the top of Home (features/home). It hides
// once the member has a session OR a started chat, or on "later". "later" and the chosen track
// live in the member's UI state in the CP (ADR 0114), so they follow the member to another
// device. GuideModal shows the same body from the account menu (the getting-started guide) after
// the card is gone, with no hide rules.
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useChatStore } from "../chat/store.ts";
import { chatList } from "../chat/api.ts";
import { openAssistantDraft } from "../chat/open.ts";
import { useUIStateStore } from "../../core/store/uiState.ts";
import { AGENTS, MEMBER_DEFAULT_AGENTS, agentOf, loginExpired } from "../../agents/registry.ts";
import { kindLaunchable } from "../../agents/launchAccess.ts";
import { useMemberCatalog } from "../settings/credentials/memberCatalog.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

// The flagship builtin assistant (workspace/agent/assistants.go: afAssistantID).
// Always present, so the "start a chat" CTA can open a draft on it without listing.
const AF_ASSISTANT_ID = "af";

interface GuideState {
  running: boolean;
  // CP unreachable (workspace fetch failed → state "unknown"). The session/chat/connection
  // probes the card relies on ALSO fail then, so "no sessions / no chats" is untrustworthy —
  // we must not read a CP outage as a fresh first-run and pop the welcome guide.
  cpDown: boolean;
  startBusy: boolean;
  gitOk: boolean;
  agentOk: boolean;
  conns: ConnectionsStatus | null; // null = still probing
  startWs(): Promise<void> | void;
  openStart(): void;
  openSettings(section?: string): void;
}

// Connections aren't kept in global state — fetch here, refetch when a connect
// action bumps connTick. On failure assume "not connected" so the card still helps.
function useGuideState(): GuideState {
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const openStart = useSessionsStore((s) => s.openStart);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const connKey = useSettingsUI((s) => s.connTick);
  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  // Which harnesses the credential policy lets this member launch (ADR 0138 D10a). Until the
  // catalogue answers (or when it cannot be vouched for) the step falls back to the login check
  // alone: the step is a hint, and the Agent refuses a forbidden create regardless.
  const { kinds } = useMemberCatalog(wsState === "running");

  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : {}))
      .catch(() => alive && setConns({}));
    return () => {
      alive = false;
    };
  }, [connKey]);

  return {
    running: wsState === "running",
    cpDown: wsState === "unknown", // fetch failed — see the field's comment
    startBusy: wsStartBusy(wsState), // start already in flight / ECS cold pull
    gitOk: !!(conns?.github?.connected || conns?.bitbucket?.connected),
    // Any kind a tenant may name completes the step, judged by the registry's own launch
    // predicate: a tenant that names Cursor must not leave its members stuck after connecting it.
    agentOk: MEMBER_DEFAULT_AGENTS.some(
      (k) =>
        (kinds ? kindLaunchable(k, { conns }, kinds) : AGENTS[k].available({ conns })) && !loginExpired(k, conns),
    ),
    conns,
    startWs,
    openStart,
    openSettings,
  };
}

interface Step {
  done: boolean;
  label: ReactNode;
  hint: string;
  cta: { text: string; on: () => void; icon?: string; glyph?: string; disabled?: boolean; title?: string } | null;
}

function StepRow({ s, next }: { s: Step; next: boolean }) {
  return (
    <li className={"onboard-step" + (s.done ? " onboard-step--done" : "") + (next ? " next" : "")}>
      <span className="onboard-mark">
        <Icon name={s.done ? "pass-filled" : next ? "arrow-right" : "circle-large-outline"} />
      </span>
      <span className="onboard-body">
        <span className="onboard-label">{s.label}</span>
        <span className="onboard-hint">{s.hint}</span>
      </span>
      {s.cta && (
        <button
          className={"onboard-cta" + (next ? " primary" : "")}
          disabled={s.cta.disabled}
          title={s.cta.title}
          onClick={s.cta.on}
        >
          {s.cta.glyph ? (
            <span className="onboard-cta-glyph" aria-hidden="true">
              {s.cta.glyph}
            </span>
          ) : (
            <Icon name={s.cta.icon!} />
          )}{" "}
          {s.cta.text}
        </button>
      )}
    </li>
  );
}

// The checklist body, shared between Home's setup card and the getting-started guide
// modal. `onNavigate` fires after a CTA that opens another surface (settings / new-session
// modal / a chat draft) — the modal closes itself there; Home just stays behind whatever
// opened.
function GuideBody({ g, onNavigate }: { g: GuideState; onNavigate?: () => void }) {
  const tr = useT();
  const onboarding = useUIStateStore((s) => s.state.onboarding);
  const patchUIState = useUIStateStore((s) => s.patch);
  // The tenant's default agent is named, not enforced: any connected agent completes the step,
  // so a member who already uses another one is never stuck on it.
  const defaultAgent = useUIStateStore((s) => s.state.defaults?.agent || "");
  // agentOf falls back to claude for a kind it does not know; a default from a newer CP must not
  // be named as claude, so an unknown one is simply not named.
  const defaultLabel = defaultAgent && agentOf(defaultAgent).id === defaultAgent ? agentOf(defaultAgent).label : "";
  const track = onboarding?.track === "dev" ? "dev" : null;
  const chooseDev = (on: boolean) =>
    void patchUIState({ onboarding: { dismissed: onboarding?.dismissed ?? false, track: on ? "dev" : "" } });
  const after = (fn: () => void) => () => {
    fn();
    onNavigate?.();
  };

  const setupDone = g.running && g.agentOk;
  const wsFirst = g.running ? undefined : tr("onb.ws_first");

  const common: Step[] = [
    {
      done: g.running,
      label: tr("onb.start_ws"),
      hint: tr("onb.start_ws_hint"),
      // Power glyph (⏻) to match the WS bar's start/stop toggle — same metaphor.
      // Inert while a start is already under way (same guard as the WS bar) so
      // mashing the CTA can't fire concurrent start POSTs.
      cta: g.running
        ? null
        : { text: g.startBusy ? tr("onb.starting") : tr("onb.start"), glyph: "⏻", disabled: g.startBusy, on: () => void g.startWs() },
    },
    {
      done: g.agentOk,
      label: tr("onb.connect_agent"),
      hint: defaultLabel ? tr("onb.connect_agent_hint_default", { agent: defaultLabel }) : tr("onb.connect_agent_hint"),
      // Connecting runs through the in-container Agent, so it needs the workspace up.
      cta: g.agentOk
        ? null
        : { text: tr("onb.connect"), icon: "plug", disabled: !g.running, title: wsFirst, on: after(() => g.openSettings("credentials")) },
    },
  ];
  const dev: Step[] = [
    {
      done: g.gitOk,
      label: (
        <>
          {tr("onb.connect_git")}<span className="onboard-opt">{tr("onb.optional")}</span>
        </>
      ),
      hint: tr("onb.connect_git_hint"),
      cta: g.gitOk
        ? null
        : { text: tr("onb.connect"), icon: "plug", disabled: !g.running, title: wsFirst, on: after(() => g.openSettings("git")) },
    },
    {
      done: false, // done = the card itself disappears (a session exists)
      label: tr("onb.clone_start"),
      hint: tr("onb.clone_start_hint"),
      cta: { text: tr("onb.get_started"), icon: "rocket", disabled: !g.running, title: wsFirst, on: after(g.openStart) },
    },
  ];

  // One highlight across the whole card: the first undone required step, then —
  // once both are done — the goal tiles, then the dev track's first open step.
  const nextCommon = !g.running ? 0 : !g.agentOk ? 1 : -1;
  const nextDev = nextCommon === -1 && track === "dev" ? (g.gitOk ? 1 : 0) : -1;
  const tilesHot = setupDone && track !== "dev";

  return (
    <>
      <ol className="onboard-steps">
        {common.map((s, i) => (
          <StepRow key={i} s={s} next={i === nextCommon} />
        ))}
      </ol>
      <div className="onboard-div">{tr("onb.which_start")}</div>
      <div className={"onboard-tiles" + (tilesHot ? " onboard-tiles-hot" : "")}>
        <div className="onboard-tile">
          <span className="onboard-tile-ic">
            <Icon name="comment-discussion" />
          </span>
          <span className="onboard-tile-title">{tr("onb.tile_chat_title")}</span>
          <span className="onboard-tile-desc">{tr("onb.tile_chat_desc")}</span>
          <button
            className={"onboard-cta" + (tilesHot ? " primary" : "")}
            disabled={!setupDone}
            title={setupDone ? undefined : tr("onb.chat_needs_setup")}
            onClick={after(() => openAssistantDraft(AF_ASSISTANT_ID))}
          >
            <Icon name="comment" /> {tr("onb.start_chat")}
          </button>
        </div>
        <div className={"onboard-tile" + (track === "dev" ? " on" : "")}>
          <span className="onboard-tile-ic">
            <Icon name="repo" />
          </span>
          <span className="onboard-tile-title">{tr("onb.tile_dev_title")}</span>
          <span className="onboard-tile-desc">{tr("onb.tile_dev_desc")}</span>
          <button className="onboard-cta" onClick={() => chooseDev(track !== "dev")}>
            <Icon name={track === "dev" ? "chevron-up" : "chevron-down"} /> {track === "dev" ? tr("onb.collapse_steps") : tr("onb.to_dev_setup")}
          </button>
        </div>
      </div>
      {track === "dev" && (
        <ol className="onboard-steps">
          {dev.map((s, i) => (
            <StepRow key={i} s={s} next={i === nextDev} />
          ))}
        </ol>
      )}
    </>
  );
}

export interface SetupState {
  /** The card should be on screen: first run, nothing dismissed, probes settled. */
  show: boolean;
  g: GuideState;
  dismiss(): void;
}

// useSetupState decides whether the first-run checklist shows. Home calls it once and hands the
// result to SetupCard, so it can also leave out its own start buttons while the card's goal
// tiles offer the same two actions.
export function useSetupState(): SetupState {
  const sessions = useSessionsStore((s) => s.sessions);
  const chatTick = useChatStore((s) => s.listTick);
  const g = useGuideState();
  const loaded = useUIStateStore((s) => s.loaded);
  const onboarding = useUIStateStore((s) => s.state.onboarding);
  const patchUIState = useUIStateStore((s) => s.patch);
  // Started chats also count as "past first-run": a chat-only user completes onboarding
  // without ever creating a session. The list lives in the workspace agent, so the probe
  // fails while the workspace is down — treat that as 0 and re-probe when it comes up (and
  // when a draft becomes a real thread: listTick).
  const [chats, setChats] = useState<number | null>(null); // null = still probing
  useEffect(() => {
    let alive = true;
    chatList()
      .then((r) => alive && setChats((r.conversations || []).filter((c) => c.message_count > 0).length))
      .catch(() => alive && setChats(0));
    return () => {
      alive = false;
    };
  }, [chatTick, g.running]);

  const show =
    !onboarding?.dismissed &&
    sessions.length === 0 &&
    (chats ?? 0) === 0 &&
    // CP down (status unknown): failed probes look identical to first-run, but they aren't.
    // The guide's launch actions could not work anyway, and a transient outage must not
    // read as a fresh install.
    !g.cpDown &&
    // Wait for the probes so checks don't flash wrong, and for the CP's UI state so a "later"
    // given on another device is not overruled by this browser's empty cache.
    g.conns !== null &&
    chats !== null &&
    loaded;
  const dismiss = () => void patchUIState({ onboarding: { dismissed: true, track: onboarding?.track ?? "" } });
  return { show, g, dismiss };
}

export function SetupCard({ setup }: { setup: SetupState }) {
  const tr = useT();
  if (!setup.show) return null;
  return (
    <section className="onboard-scope onboard-card home-setup">
      <div className="onboard-title">{tr("onb.welcome")}</div>
      <div className="onboard-sub">{tr("onb.welcome_sub")}</div>
      <GuideBody g={setup.g} />
      <div className="onboard-foot">
        <button className="ghost" onClick={setup.dismiss}>
          {tr("onb.later")}
        </button>
      </div>
    </section>
  );
}

// Getting-started guide — the same checklist as a plain modal, reachable from the
// account menu any time (the first-run card is gone once dismissed / set up, but
// the guide should stay consultable). No dismiss / auto-hide rules here.
export function GuideModal() {
  const tr = useT();
  const closeGuide = useSettingsUI((s) => s.closeGuide);
  const g = useGuideState();
  return (
    <Modal title={tr("onb.guide_title")} onClose={closeGuide} className="guide-modal">
      <div className="ui-modal-body onboard-scope">
        <div className="onboard-sub">{tr("onb.guide_sub")}</div>
        {g.conns !== null && <GuideBody g={g} onNavigate={closeGuide} />}
      </div>
    </Modal>
  );
}
