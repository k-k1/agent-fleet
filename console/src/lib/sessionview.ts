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

// exitLabel describes why a stopped session's agent process died, when the pane
// recorder caught an abnormal end. Returns null for a clean quit or a deliberate stop
// (no reason recorded) — those keep the plain 停止中 chip.
export const exitLabel = (s: Session): { text: string; hint: string } | null => {
  switch (s.exitReason) {
    case "oom":
      return {
        text: "メモリ不足で終了",
        hint: `メモリ不足でプロセスが強制終了されました（OOM kill / exit ${s.exitCode ?? 137}）。ワークスペースのメモリ上限に達した可能性があります。`,
      };
    case "killed":
      return {
        text: "強制終了",
        hint: `プロセスが SIGKILL で強制終了されました（signal ${s.exitSignal ?? 9}）。ホスト全体のメモリ逼迫などが原因の可能性があります。`,
      };
    case "crashed":
      return {
        text: "異常終了",
        hint: s.exitSignal
          ? `プロセスが signal ${s.exitSignal} で異常終了しました。`
          : `プロセスが異常終了しました（exit code ${s.exitCode ?? "?"}）。`,
      };
    default:
      return null;
  }
};

// stateInfo maps a session to its status chip (codicon + label + color class).
export const stateInfo = (s: Session): StateInfo => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", icon: "circle-slash", text: "フォルダ無し — 再開不可" };
    // An abnormal end (crash / OOM) gets a warning chip so the row stands out from a
    // clean 停止中; the reason detail rides the row tooltip.
    const ex = exitLabel(s);
    if (ex) return { cls: "off warn", icon: "warning", text: ex.text };
    return { cls: "off", icon: "debug-pause", text: "停止中" };
  }
  // shell has no working/idle state model — alive means it's running.
  if (agentOf(s.kind).caps.fixedAliveChip) return { cls: "on", icon: "pulse", text: "起動中" };
  // claude (hooks), opencode (plugin) and codex (injected hooks) all report
  // working/idle. opencode (a running question tool in its store) and codex (an
  // unanswered request_user_input in the rollout tail) also derive "question".
  // An empty state = idle.
  switch (s.state) {
    case "compacting":
      return { cls: "working", icon: "loading", spin: true, text: "圧縮中…" };
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
