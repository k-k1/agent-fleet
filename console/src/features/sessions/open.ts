// Session-open helpers: the navigation verbs the rail (and later the mirror /
// modals) use to put a session on screen. Old showTerminal/showTerminalSplit.
//
// TODO(P5): the old console opened chat-capable sessions (claude) in the chat
// mirror by default (showChat/showChatSplit). Until MirrorView is ported, every
// open lands on the terminal; a stopped session shows the resume mask instead of
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
// history shows read-only without resuming ("resume and continue" resumes explicitly).
export function openSessionChat(name: string): void {
  useLayoutStore.getState().openTarget({ content: { kind: "terminal", chat: true }, session: name });
}

export function openSessionChatSplit(name: string): void {
  useLayoutStore.getState().openTargetInNew({ content: { kind: "terminal", chat: true }, session: name });
}

// openSessionDefault — the ordinary way to open a running session: a chat-capable kind
// (claude and friends) opens the mirror, anything else the terminal. Same rule as clicking
// a row in the left rail (SessionRow), so that other routes in — rotating by swipe — do not
// land on a different surface.
export function openSessionDefault(s: Session): void {
  (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(s.name);
}

// openSessionFromList — where a row from a list opens. The destination depends on whether
// the session is alive, whether the kind has a transcript, and whether its working folder
// still exists, so having the left rail's rows (SessionRow) and the command palette's
// session rows each spell the conditions out would drift. This is the single source of truth.
//
//   alive                    → mirror for a chat-capable kind, terminal otherwise
//   stopped, has transcript  → read-only history (no resume; the transcript lives in the
//                              agent's home, so it opens even when the folder is gone)
//   stopped, no transcript   → terminal replay, but only when the folder still exists
//                              (resumable !== false) and the workspace is running
//
// running = whether the workspace is running. Returns false when nothing could be opened, so
// the caller does not silently let a click do nothing.
export function openSessionFromList(s: Session, split: boolean, running: boolean): boolean {
  const caps = agentOf(s.kind).caps;
  if (s.alive) {
    (caps.chat
      ? split ? openSessionChatSplit : openSessionChat
      : split ? openSessionTerminalSplit : openSessionTerminal)(s.name);
    return true;
  }
  if (caps.transcript) {
    (split ? openSessionChatSplit : openSessionChat)(s.name);
    return true;
  }
  if (s.resumable !== false && running) {
    (split ? openSessionTerminalSplit : openSessionTerminal)(s.name);
    return true;
  }
  return false;
}

/** Rotates the running sessions by delta and opens the result in the active pane — swapping
 * which session the pane shows, not moving between panes (docs/log/29). Returns the
 * destination, or null when there is no candidate or only one, so the caller can say "there
 * are no others". */
export function rotateRunningSession(delta: number): RotateTarget | null {
  const list = rotatableSessions(useSessionsStore.getState().sessions, activeWorkingSet(getSettings()));
  const current = activePane(useLayoutStore.getState().layout)?.session;
  const target = rotateTarget(list, current, delta);
  if (target) openSessionDefault(target.session);
  return target;
}

// The first prompt is NOT delivered from here. Waiting for alive in the browser only runs
// while the Console is open, and typing a fixed delay after alive (tmux exists) can be eaten
// by the CLI's startup screen. The Agent delivers it from the create request's
// initial_prompt (or /input {when_ready} when there are attachments), with the wait, the
// second Enter and a delivery check, and it runs with the Console closed (useStartWork.ts).
