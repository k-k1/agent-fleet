// Pure, DOM-free helpers for the schedules rail section (docs/log/38 P5). Split out from
// the component so the formatting + status classification is unit-testable (the codebase
// tests pure logic, not React rendering — see notifications/read.test.ts). The section
// component owns fetch/poll/state; everything here is a plain function over the DTO.
import { t, type MsgKey } from "../../lib/i18n/index.ts";

// Wire shape returned by GET /api/schedules — mirrors the CP scheduleDTO (schedule.go).
// The list response already carries every field below; the detail/edit modal (P5.2) reads
// the reuse/rotation/target fields for its read-only section and patches the editable ones.
export interface ScheduleDTO {
  id: string;
  spec_kind: "cron" | "interval" | "once" | string;
  spec: string;
  spec_label?: string;
  tz?: string;
  wake_policy?: string;
  session_mode?: "new" | "reuse" | string;
  reuse_target?: string;
  agent_kind?: string;
  model?: string;
  repo?: string;
  worktree?: string;
  new_branch?: boolean;
  prompt?: string;
  overlap_policy?: string;
  rotation?: string;
  missing_target_policy?: string;
  reuse_session?: string;
  reuse_run_count?: number;
  owner_conv?: string;
  // Completion-report opt-in: true = the fire's session reports back to the owner
  // (operator/assistant) conversation. Default false = fire silently.
  report?: boolean;
  enabled: boolean;
  next_run?: string;
  next_run_local?: string;
  last_run?: string;
  last_status?: string;
  created_at?: string;
  updated_at?: string;
  // Set by run-now/create when the CP scheduler goroutine is disabled on this deployment —
  // the schedule is stored but will never fire until an operator enables it. Relayed to the
  // user as a warning toast.
  warning?: string;
}

// The subset the detail/edit modal patches — the structured fields that need no NL->spec
// translation. Sent to PATCH /api/schedules/{id}; omitted keys are left unchanged (the CP
// schedulePatch treats a missing field as "no change"), so the modal only sends what the
// user actually edited (and never resets next_run by re-submitting an unchanged spec).
export interface ScheduleEditable {
  spec_kind?: string;
  spec?: string;
  tz?: string;
  spec_label?: string;
  prompt?: string;
  wake_policy?: string;
  agent_kind?: string;
  model?: string;
  report?: boolean;
}

// One row from GET /api/schedules/{id}/runs.
export interface ScheduleRun {
  fired_at: string;
  status: string;
  detail?: string;
  // The session this fire drove (created for session_mode=new, the reuse target for
  // session_mode=reuse) — the history can open it. Empty for soft skips that ran nothing.
  session?: string;
  // How the fire was initiated: "manual" (a run-now) or "scheduled" (an automatic fire).
  trigger?: string;
}

// --- List payload classification -------------------------------------------------
// api() RESOLVES a CP error as { error } instead of throwing, so "not an array" is the
// normal shape of a 401 / 5xx here. Reading that as an empty list is how a failed fetch
// turned into 「定時実行はまだありません」 — the one message that makes a user believe the
// schedule they just created is gone. Callers keep the rows they already have and show
// the failure instead.
export interface ScheduleListResult {
  /** Rows to adopt, or null when nothing could be read (keep the previous ones). */
  items: ScheduleDTO[] | null;
  /** The CP's error payload when there is one, for the caller to run through errText.
   * null when the payload was neither a list nor an {error} (an old CP / a proxy page)
   * — still a failure, just without a reason to quote. Rendering stays the caller's job
   * so this module keeps its no-DOM, no-api-client contract. */
  error: { code?: string; message?: string } | string | null;
}

export function readScheduleList(res: unknown): ScheduleListResult {
  if (Array.isArray(res)) return { items: res as ScheduleDTO[], error: null };
  const err = (res as { error?: { code?: string; message?: string } | string } | null | undefined)?.error;
  return { items: null, error: err ?? null };
}

// A run/last_status token maps to one of four tones so the dot + label read consistently
// with the rest of the console (--ok / --warn / --danger / --muted). The scheduler emits:
// "fired"/"fired_noop" (success), "skipped_*" (a soft skip), "error:*" (a hard failure),
// and "" (never run yet).
export type StatusTone = "ok" | "warn" | "danger" | "muted";

export function statusTone(status?: string): StatusTone {
  const s = (status || "").trim();
  if (s === "") return "muted";
  if (s.startsWith("error")) return "danger";
  if (s.startsWith("skipped")) return "warn";
  if (s.startsWith("fired")) return "ok";
  return "muted";
}

// The codicon for a status tone (matches the tone semantics used across the console).
export function statusIcon(status?: string): string {
  switch (statusTone(status)) {
    case "ok":
      return "pass-filled";
    case "danger":
      return "error";
    case "warn":
      return "circle-slash";
    default:
      return "circle-outline";
  }
}

// The i18n key for a run's outcome, keyed off the same four tones as the status dot so
// history reads "成功 / 失敗 / スキップ / 未実行" instead of the raw token (which stays in the
// row tooltip). Pure so it is unit-tested alongside statusTone.
export function runStatusLabelKey(status?: string): MsgKey {
  switch (statusTone(status)) {
    case "ok":
      return "sched.status_ok";
    case "danger":
      return "sched.status_fail";
    case "warn":
      return "sched.status_skip";
    default:
      return "sched.status_pending";
  }
}

// A run is "manual" when it was triggered by run-now; anything else is an automatic
// scheduled fire. The CP stamps trigger="manual"/"scheduled"; an older row with no
// trigger reads as scheduled (the pre-existing behavior before run-now was tagged).
export function isManualRun(trigger?: string): boolean {
  return trigger === "manual";
}

// A schedule is "paused" when it is disabled; the enabled flag is the single source of
// truth (a paused row also has an empty next_run, but enabled is what the toggle flips).
export function isPaused(s: ScheduleDTO): boolean {
  return !s.enabled;
}

// Human label for a schedule. Prefer the operator's original natural-language label
// (spec_label, e.g. "毎朝9時レビュー"); fall back to a terse spec summary so a row is
// never blank.
export function scheduleTitle(s: ScheduleDTO): string {
  const label = (s.spec_label || "").trim();
  if (label) return label;
  return specSummary(s);
}

// A terse, human-readable summary of the raw spec — used when there is no spec_label and
// for the row's secondary line. interval seconds are rendered as a compact duration.
export function specSummary(s: ScheduleDTO): string {
  switch (s.spec_kind) {
    case "cron":
      return s.spec;
    case "interval":
      // 「〜ごと」の言い回しはロケール依存なのでカタログへ（t() は非 React からも呼べる）。
      return t("sched.every", { interval: formatInterval(s.spec) });
    case "once":
      return s.spec;
    default:
      return s.spec || s.spec_kind;
  }
}

// Render an interval given as whole seconds into "90m" / "6h" / "1d 2h" style. Falls back
// to "<n>s" for odd values so a malformed spec still shows something.
export function formatInterval(spec: string): string {
  const secs = Number.parseInt((spec || "").trim(), 10);
  if (!Number.isFinite(secs) || secs <= 0) return spec;
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  const parts: string[] = [];
  if (d) parts.push(d + "d");
  if (h) parts.push(h + "h");
  if (m) parts.push(m + "m");
  if (s && !d && !h) parts.push(s + "s");
  return parts.join(" ") || secs + "s";
}

// Sort for display: enabled schedules first (paused sink to the bottom), then by the
// soonest next_run. A row with no next_run sorts after ones that have one. Stable on id.
export function sortSchedules(list: ScheduleDTO[]): ScheduleDTO[] {
  return [...list].sort((a, b) => {
    if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
    const an = a.next_run || "";
    const bn = b.next_run || "";
    if (an && bn && an !== bn) return an < bn ? -1 : 1;
    if (an !== bn) return an ? -1 : 1; // one has a next_run, the other doesn't
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}
