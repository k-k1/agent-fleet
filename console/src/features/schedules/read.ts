// Pure, DOM-free helpers for the schedules rail section (docs/38 P5). Split out from
// the component so the formatting + status classification is unit-testable (the codebase
// tests pure logic, not React rendering — see notifications/read.test.ts). The section
// component owns fetch/poll/state; everything here is a plain function over the DTO.

// Wire shape returned by GET /api/schedules — mirrors the CP scheduleDTO (schedule.go).
export interface ScheduleDTO {
  id: string;
  spec_kind: "cron" | "interval" | "once" | string;
  spec: string;
  spec_label?: string;
  tz?: string;
  wake_policy?: string;
  agent_kind?: string;
  model?: string;
  repo?: string;
  enabled: boolean;
  next_run?: string;
  next_run_local?: string;
  last_run?: string;
  last_status?: string;
  // Set by run-now/create when the CP scheduler goroutine is disabled on this deployment —
  // the schedule is stored but will never fire until an operator enables it. Relayed to the
  // user as a warning toast.
  warning?: string;
}

// One row from GET /api/schedules/{id}/runs.
export interface ScheduleRun {
  fired_at: string;
  status: string;
  detail?: string;
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
      return "every " + formatInterval(s.spec);
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
