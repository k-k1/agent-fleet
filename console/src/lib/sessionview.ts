// Presentation helpers for a session object, shared by the left-pane Sessions
// list and the terminal pane header so both render a session identically: a
// display name, and a status chip (codicon + label + color class). Per-kind
// nuances come from the agent registry (fixedAliveChip / namedByLabel) instead of
// inline `kind === "shell"` checks.
import { agentOf } from "../agents/registry.ts";
import type { StateInfo } from "../agents/registry.ts";
import type { Session } from "../types/session.ts";
import { t } from "./i18n/index.ts";

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

// What stateInfo actually reads. Structural on purpose: Session satisfies it, and so
// does the recipient-side SharedSession (docs/59) — a shared row must show the same
// 進行中 / 入力待ち / 質問中 chip as the owner's own rail, from the same code, or the two
// sides drift apart in exactly the way that makes a shared list untrustworthy.
export interface SessionState {
  kind?: string;
  alive?: boolean;
  state?: string;
  resumable?: boolean;
  backgroundBusy?: boolean;
  exitReason?: string;
  exitCode?: number;
  exitSignal?: number;
  rateLimitResumeAt?: string;
}

// resumeClock renders a reserved resume instant for the 制限解除待ち chip: "19:50",
// or "08/20 07:15" when it is not today (a weekly window can land days out, and a bare
// time would then read as "in a few minutes"). "" for absent/unparsable input — the
// chip then says only that the session is waiting, which is still true.
export const resumeClock = (iso: string | undefined, now: Date = new Date()): string => {
  const d = new Date(iso ?? "");
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`;
  return d.toDateString() === now.toDateString() ? hm : `${p(d.getMonth() + 1)}/${p(d.getDate())} ${hm}`;
};

// exitLabel describes why a stopped session's agent process died, when the pane
// recorder caught an abnormal end. Returns null for a clean quit or a deliberate stop
// (no reason recorded) — those keep the plain 停止中 chip.
export const exitLabel = (s: SessionState): { text: string; hint: string } | null => {
  switch (s.exitReason) {
    case "oom":
      return {
        text: t("exit.oom.text"),
        hint: t("exit.oom.hint", { code: s.exitCode ?? 137 }),
      };
    case "killed":
      return {
        text: t("exit.killed.text"),
        hint: t("exit.killed.hint", { signal: s.exitSignal ?? 9 }),
      };
    case "crashed":
      return {
        text: t("exit.crashed.text"),
        hint: s.exitSignal
          ? t("exit.crashed.hint_signal", { signal: s.exitSignal })
          : t("exit.crashed.hint_code", { code: s.exitCode ?? "?" }),
      };
    default:
      return null;
  }
};

// stateInfo maps a session to its status chip (codicon + label + color class).
export const stateInfo = (s: SessionState): StateInfo => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", icon: "circle-slash", text: t("state.folder_missing") };
    // An abnormal end (crash / OOM) gets a warning chip so the row stands out from a
    // clean 停止中; the reason detail rides the row tooltip.
    const ex = exitLabel(s);
    if (ex) return { cls: "off warn", icon: "warning", text: ex.text };
    return { cls: "off", icon: "debug-pause", text: t("state.stopped") };
  }
  // shell has no working/idle state model — alive means it's running.
  if (agentOf(s.kind).caps.fixedAliveChip) return { cls: "on", icon: "pulse", text: t("state.running") };
  // claude (hooks), opencode (plugin) and codex (injected hooks) all report
  // working/idle. opencode (a running question tool in its store) and codex (an
  // unanswered request_user_input in the rollout tail) also derive "question".
  // An empty state = idle.
  switch (s.state) {
    case "compacting":
      return { cls: "working", icon: "loading", spin: true, text: t("state.compacting") };
    case "working":
      return { cls: "working", icon: "loading", spin: true, text: t("state.working") };
    case "question":
      return { cls: "question", icon: "question", text: t("state.question") };
    case "plan":
      return { cls: "question", icon: "checklist", text: t("state.plan") };
    case "permission":
      return { cls: "question", icon: "shield", text: t("state.permission") };
    // The CLI parked the pane on a menu only a human keypress clears (claude's usage-limit
    // menu). Grouped with the question colours because it is an attention state: the turn
    // is over and nothing moves until someone acts in the pane. Deliberately NOT "idle" —
    // the backend refuses prompt injection here, since typed text would land on the menu.
    case "blocked":
      return { cls: "question", icon: "debug-disconnect", text: t("state.blocked") };
    // The turn was cut off by a usage limit and the session is simply waiting for the
    // window to reset (claude: the menu is already auto-dismissed, or the limit never
    // showed one; codex managed: usageLimitExceeded). Deliberately NOT "idle" — it looked
    // exactly like a finished turn, so nothing on screen said why the session stopped or
    // when it moves again (docs/47 §4-9). Separate from "blocked" because nobody has to
    // act: rateLimitResumeAt is when the reserved auto-resume fires. It keeps the question
    // colours (so the row is readable at a glance as "not running") but adds "limited",
    // which drops the bold weight — it must not shout as loudly as an unanswered question.
    case "limited": {
      const at = resumeClock(s.rateLimitResumeAt);
      return {
        cls: "question limited",
        icon: "watch",
        text: at ? t("state.rate_limited_at", { at }) : t("state.rate_limited"),
      };
    }
    // The usage limit that WAITING never clears: the org's monthly spend limit / an
    // exhausted credit balance (agent: agents.StateSpendLimit, docs/47 §4-10). It arrives
    // as the same 429 as the time windows above, so only the wording tells them apart —
    // and showing 制限解除待ち here would park the user on a reset that never comes. The
    // next move is a billing one (raise the limit / add credits), so it takes the loud
    // question colours like blocked / auth rather than the calmer limited tint.
    case "spend_limit":
      return { cls: "question", icon: "credit-card", text: t("state.spend_limit") };
    // The workspace's claude login expired (agent: agents.StateAuth). Separate from
    // "blocked" because the next move is the opposite one: a usage limit lifts on its
    // own, an expired login never does — it needs 再認証 now. NOT "idle" for the same
    // reason as blocked, only worse: the pane looks like it is waiting for input, but a
    // prompt sent here is accepted and no turn ever starts (the backend now refuses it).
    case "auth":
      return { cls: "question", icon: "key", text: t("state.auth_expired") };
    default:
      // Idle by hook, but a run_in_background task is still running under the pane:
      // show it's not actually done (spinner + "BG実行中" alongside 入力待ち).
      if (s.backgroundBusy) return { cls: "bg", icon: "loading", spin: true, text: t("state.idle_bg") };
      return { cls: "on", icon: "check", text: t("state.idle") };
  }
};
