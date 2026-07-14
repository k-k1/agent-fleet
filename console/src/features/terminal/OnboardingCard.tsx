// OnboardingCard — first-run guide, v2 (起動導線再設計 Phase 1). The old card was a
// single developer-shaped checklist (WS → git → agent → session) that chat-only
// users could never complete (it only auto-hid on session creation) and that
// docs/guide/lite.md had to patch in prose ("ステップ2と4は飛ばして大丈夫").
// v2 splits it into the two steps everyone needs — start workspace, connect an
// agent — followed by a goal choice: chat (repo-less Q&A / translation, done in
// one click) or development (git → clone → first session, expanded on demand).
// The card hides once the user has a session OR a started chat, or on "あとで"
// (remembered in localStorage). The same body is reused by GuideModal, reachable
// from the account menu as 「はじめかたガイド」 after dismissal.
// Rendered only on the active empty pane (see TerminalView) so it shows just once.
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../../core/api/client.ts";
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
    startBusy: wsStartBusy(wsState), // start already in flight / ECS cold pull
    gitOk: !!(conns?.github?.connected || conns?.bitbucket?.connected),
    agentOk: !!(
      conns?.claude?.connected ||
      conns?.codex?.connected ||
      (conns?.opencode?.envs?.length ?? 0) > 0
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
  const wsFirst = g.running ? undefined : "先にワークスペースを起動してください";

  const common: Step[] = [
    {
      done: g.running,
      label: "ワークスペースを起動",
      hint: "あなた専用のコンテナを立ち上げます",
      // Power glyph (⏻) to match the WS バー's start/stop toggle — same metaphor.
      // Inert while a start is already under way (same guard as the WS バー) so
      // mashing the CTA can't fire concurrent start POSTs.
      cta: g.running
        ? null
        : { text: g.startBusy ? "起動中…" : "起動", glyph: "⏻", disabled: g.startBusy, on: () => void g.startWs() },
    },
    {
      done: g.agentOk,
      label: "エージェントを接続",
      hint: "Claude / Codex / opencode のいずれかにサインイン",
      // Connecting runs through the in-container Agent, so it needs the workspace up.
      cta: g.agentOk
        ? null
        : { text: "接続する", icon: "plug", disabled: !g.running, title: wsFirst, on: after(() => g.openSettings("agents")) },
    },
  ];
  const dev: Step[] = [
    {
      done: g.gitOk,
      label: (
        <>
          git プロバイダを接続<span className="onboard-opt">任意</span>
        </>
      ),
      hint: "private リポジトリをクローン / push するなら接続します",
      cta: g.gitOk
        ? null
        : { text: "接続する", icon: "plug", disabled: !g.running, title: wsFirst, on: after(() => g.openSettings("git")) },
    },
    {
      done: false, // done = the card itself disappears (a session exists)
      label: "リポジトリをクローンしてセッション開始",
      hint: "クローンと起動は「はじめる」からまとめて行えます",
      cta: { text: "はじめる", icon: "rocket", disabled: !g.running, title: wsFirst, on: after(g.openStart) },
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
      <div className="onboard-div">どちらから始めますか？ — あとから両方使えます</div>
      <div className={"onboard-tiles" + (tilesHot ? " hot" : "")}>
        <div className="onboard-tile">
          <span className="onboard-tile-ic">
            <Icon name="comment-discussion" />
          </span>
          <span className="onboard-tile-title">AI に質問・翻訳を頼む</span>
          <span className="onboard-tile-desc">使い捨てのチャット。git もターミナルも不要で、そのまま使えます。</span>
          <button
            className={"onboard-cta" + (tilesHot ? " primary" : "")}
            disabled={!setupDone}
            title={setupDone ? undefined : "上の2ステップを済ませると使えます"}
            onClick={after(() => openAssistantDraft(AF_ASSISTANT_ID))}
          >
            <Icon name="comment" /> チャットをはじめる
          </button>
        </div>
        <div className={"onboard-tile" + (track === "dev" ? " on" : "")}>
          <span className="onboard-tile-ic">
            <Icon name="repo" />
          </span>
          <span className="onboard-tile-title">リポジトリで開発する</span>
          <span className="onboard-tile-desc">git を接続し、リポジトリをクローンして AI セッションを起動します。</span>
          <button className="onboard-cta" onClick={() => chooseDev(track !== "dev")}>
            <Icon name={track === "dev" ? "chevron-up" : "chevron-down"} /> {track === "dev" ? "手順をたたむ" : "開発のセットアップへ"}
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
        <div className="onboard-title">Agent Fleet へようこそ</div>
        <div className="onboard-sub">まず2ステップ。そのあとは目的を選ぶだけです</div>
        <GuideBody g={g} />
        <div className="onboard-foot">
          <button className="ghost" onClick={dismiss}>
            あとで
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
  const closeGuide = useSettingsUI((s) => s.closeGuide);
  const g = useGuideState();
  return (
    <Modal title="はじめかたガイド" onClose={closeGuide} className="guide-modal">
      <div className="ui-modal-body onboard-scope">
        <div className="onboard-sub">済んだ項目には自動でチェックが付きます</div>
        {g.conns !== null && <GuideBody g={g} onNavigate={closeGuide} />}
      </div>
    </Modal>
  );
}
