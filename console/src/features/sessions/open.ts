// Session-open helpers: the navigation verbs the rail (and later the mirror /
// modals) use to put a session on screen. Old showTerminal/showTerminalSplit.
//
// TODO(P5): the old console opened chat-capable sessions (claude) in the chat
// mirror by default (showChat/showChatSplit). Until MirrorView is ported, every
// open lands on the terminal; a stopped session shows the 再開 mask instead of
// read-only history.
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { reconnectSession } from "../../terminal/service.ts";
import { agentOf } from "../../agents/registry.ts";
import { getSettings } from "../../lib/settings.ts";
import { activeWorkingSet } from "../../lib/workingSetsStore.ts";
import { useSessionsStore } from "./store.ts";
import { rotatableSessions, rotateTarget } from "./rotate.ts";
import type { RotateTarget } from "./rotate.ts";
import type { Session } from "../../types/session.ts";

export function openSessionTerminal(name: string): void {
  useLayoutStore.getState().openTarget({ content: { kind: "terminal", chat: false }, session: name });
  // Re-clicking an already-open but disconnected session doesn't change the
  // pane's props (the declarative attach won't re-run) — revive its dropped
  // socket here so clicking a "[disconnected]" row reconnects it.
  reconnectSession(name);
}

export function openSessionTerminalSplit(name: string): void {
  useLayoutStore.getState().openTargetInNew({ content: { kind: "terminal", chat: false }, session: name });
  reconnectSession(name);
}

// Chat-mirror opens: a chat-capable session (claude) opens the Markdown mirror
// by default — alive: the PTY still attaches in the background; stopped: the
// history shows read-only without resuming (再開して続ける resumes explicitly).
export function openSessionChat(name: string): void {
  useLayoutStore.getState().openTarget({ content: { kind: "terminal", chat: true }, session: name });
}

export function openSessionChatSplit(name: string): void {
  useLayoutStore.getState().openTargetInNew({ content: { kind: "terminal", chat: true }, session: name });
}

// openSessionDefault — 稼働中セッションの「ふつうの開き方」: チャットできる種
// （claude 等）はミラー、それ以外はターミナル。左ペインの行クリック（SessionRow）と
// 同じ規則で、行以外の導線（スワイプでのローテート）が別の面を開かないようにする。
export function openSessionDefault(s: Session): void {
  (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(s.name);
}

/** 稼働中セッションを delta 個ぶんローテートし、アクティブなペインに開く（docs/29 の
 * ペイン移動ではなく「セッションの持ち替え」）。行き先を返す — 対象が無い／1 件しか
 * 無ければ null で、呼び手はそのまま「他にありません」と伝えればよい。 */
export function rotateRunningSession(delta: number): RotateTarget | null {
  const list = rotatableSessions(useSessionsStore.getState().sessions, activeWorkingSet(getSettings()));
  const current = activePane(useLayoutStore.getState().layout)?.session;
  const target = rotateTarget(list, current, delta);
  if (target) openSessionDefault(target.session);
  return target;
}

// 起動時の最初の指示をここから配達していた sendPromptWhenAlive は撤去した。ブラウザ側で
// alive を待って打つやり方は「Console が開いている間しか走らない」うえ、alive（tmux が
// 在る）から一定時間で打つだけなので起動画面に食われることがある。今は作成要求の
// initial_prompt（添付ありのみ /input {when_ready}）で Agent が配達する — 待ち・二度目の
// Enter・配達確認つきで、Console を閉じていても走る（useStartWork.ts）。
