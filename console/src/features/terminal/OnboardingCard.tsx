// OnboardingCard — first-run guide, v2 (起動導線再設計 Phase 1). The old card was a
// single developer-shaped checklist (WS → git → agent → session) that chat-only
// users could never complete (it only auto-hid on session creation) and that
// guide/member/lite.md had to patch in prose ("ステップ2と4は飛ばして大丈夫").
// v2 splits it into the two steps everyone needs — start workspace, connect an
// agent — followed by a goal choice: chat (repo-less Q&A / translation, done in
// one click) or development (git → clone → first session, expanded on demand).
// The card hides once the user has a session OR a started chat, or on "あとで"
// (remembered in localStorage). The same body is reused by GuideModal, reachable
// from the account menu as 「はじめかたガイド」 after dismissal.
// Rendered on the active empty pane so it shows just once — BOTH kinds of empty:
// a blank terminal pane (TerminalView) and a cell with no view at all
// (panes/Pane.tsx の EmptyPane)。★ 後者が新規ユーザーの初期レイアウト（ops.ts の
// emptyCell は views: [] ＝ペインが 1 枚も無い）で、TerminalView 側にしか置いていな
// かった間は、初回のガイドがいちばん見せたい相手にだけ出ていなかった。
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
import type { ConnectionsStatus } from "../../types/session.ts";

const DISMISS_KEY = "af.onboarding.dismissed";
const TRACK_KEY = "af.onboarding.track";
// The flagship builtin assistant (workspace/agent/assistants.go: afAssistantID).
// Always present, so チャットをはじめる can open a draft on it without listing.
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
    agentOk: !!(
      conns?.claude?.connected ||
      conns?.codex?.connected ||
      // opencode は APIキー（env）と opencode アカウント（OAuth）のどちらでも成立する。
      (conns?.opencode?.envs?.length ?? 0) > 0 ||
      !!conns?.opencode?.oauth
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
    <li className={"onboard-step" + (s.done ? " done" : "") + (next ? " next" : "")}>
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

// The checklist body, shared between the first-run overlay and the はじめかた
// ガイド modal. `onNavigate` fires after a CTA that opens another surface
// (settings / new-session modal / a chat draft) — the modal closes itself there;
// the overlay just stays behind whatever opened.
function GuideBody({ g, onNavigate }: { g: GuideState; onNavigate?: () => void }) {
  const tr = useT();
  const [track, setTrack] = useState<"dev" | null>(() => {
    try {
      return localStorage.getItem(TRACK_KEY) === "dev" ? "dev" : null;
    } catch {
      return null;
    }
  });
  const chooseDev = (on: boolean) => {
    try {
      if (on) localStorage.setItem(TRACK_KEY, "dev");
      else localStorage.removeItem(TRACK_KEY);
    } catch {}
    setTrack(on ? "dev" : null);
  };
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
      // Power glyph (⏻) to match the WS バー's start/stop toggle — same metaphor.
      // Inert while a start is already under way (same guard as the WS バー) so
      // mashing the CTA can't fire concurrent start POSTs.
      cta: g.running
        ? null
        : { text: g.startBusy ? tr("onb.starting") : tr("onb.start"), glyph: "⏻", disabled: g.startBusy, on: () => void g.startWs() },
    },
    {
      done: g.agentOk,
      label: tr("onb.connect_agent"),
      hint: tr("onb.connect_agent_hint"),
      // Connecting runs through the in-container Agent, so it needs the workspace up.
      cta: g.agentOk
        ? null
        : { text: tr("onb.connect"), icon: "plug", disabled: !g.running, title: wsFirst, on: after(() => g.openSettings("agents")) },
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
      <div className={"onboard-tiles" + (tilesHot ? " hot" : "")}>
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

export function OnboardingCard() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const chatTick = useChatStore((s) => s.listTick);
  const g = useGuideState();
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(DISMISS_KEY) === "1";
    } catch {
      return false;
    }
  });
  // Started chats also count as "past first-run" (v2): a chat-only user completes
  // onboarding without ever creating a session. The list lives in the workspace
  // agent, so the probe fails while the workspace is down — treat that as 0 and
  // re-probe when it comes up (and when a draft becomes a real thread: listTick).
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

  if (dismissed || sessions.length > 0 || (chats ?? 0) > 0) return null;
  // CP down (status 不明): the empty panes and failed probes look identical to first-run,
  // but they aren't — don't show the welcome guide (its launch actions can't work anyway,
  // and it misreads a transient outage as a fresh install). The pane keeps its neutral
  // "セッション未接続" empty state; the WS bar already signals 不明.
  if (g.cpDown) return null;
  if (g.conns === null || chats === null) return null; // wait for the probes so checks don't flash wrong

  const dismiss = () => {
    try {
      localStorage.setItem(DISMISS_KEY, "1");
    } catch {}
    setDismissed(true);
  };

  return (
    <div className="onboard">
      <div className="onboard-card">
        <div className="onboard-title">{tr("onb.welcome")}</div>
        <div className="onboard-sub">{tr("onb.welcome_sub")}</div>
        <GuideBody g={g} />
        <div className="onboard-foot">
          <button className="ghost" onClick={dismiss}>
            {tr("onb.later")}
          </button>
        </div>
      </div>
    </div>
  );
}

// はじめかたガイド — the same checklist as a plain modal, reachable from the
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
