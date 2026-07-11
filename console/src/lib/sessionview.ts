// Presentation helpers for a session object, shared by the left-pane Sessions
// list and the terminal pane header so both render a session identically: a
// display name, and a status chip (codicon + label + color class). Per-kind
// nuances come from the agent registry (fixedAliveChip / namedByLabel) instead of
// inline `kind === "shell"` checks.
import { agentOf } from "../agents/registry.ts";
import type { StateInfo } from "../agents/registry.ts";
import type { Session } from "../types/session.ts";

// stamp formats a timestamp as MMDD-HHMM (matching the agent's claude --name), so
// shell rows show a launch time consistent with claude rows.
export const stamp = (iso: string | undefined): string => {
  const d = new Date(iso ?? "");
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
};

// displayName: the user-supplied title when set (any kind); else a claude session's
// --name minus the "[AF] " tag; else "{repo} @MMDD-HHMM". The kind is shown by the
// badge, so no [AF]/[SH] prefix is needed. namedByLabel is false only for shell.
export const displayName = (s: Session): string => {
  if (s.title) return s.title;
  if (agentOf(s.kind).caps.namedByLabel && s.label) return s.label.replace(/^\[AF\]\s*/, "");
  return `${s.repo || s.name} @${stamp(s.createdAt)}`;
};

// stateInfo maps a session to its status chip (codicon + label + color class).
export const stateInfo = (s: Session): StateInfo => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", icon: "circle-slash", text: "フォルダ無し — 再開不可" };
    return { cls: "off", icon: "debug-pause", text: "停止中" };
  }
  // shell has no working/idle state model — alive means it's running.
  if (agentOf(s.kind).caps.fixedAliveChip) return { cls: "on", icon: "pulse", text: "起動中" };
  // claude (hooks), opencode (plugin) and codex (injected hooks) all report
  // working/idle. opencode also derives "question" from its store (a running question
  // tool); codex has no question state. An empty state = idle.
  switch (s.state) {
    case "working":
      return { cls: "working", icon: "loading", spin: true, text: "進行中…" };
    case "question":
      return { cls: "question", icon: "question", text: "質問あり" };
    case "plan":
      return { cls: "question", icon: "checklist", text: "プランあり" };
    case "permission":
      return { cls: "question", icon: "shield", text: "許可待ち" };
    default:
      // Idle by hook, but a run_in_background task is still running under the pane:
      // show it's not actually done (spinner + "BG実行中" alongside 入力待ち).
      if (s.backgroundBusy) return { cls: "bg", icon: "loading", spin: true, text: "入力待ち · BG実行中" };
      return { cls: "on", icon: "check", text: "入力待ち" };
  }
};
