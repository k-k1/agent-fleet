// The notification settings table (Settings › Notifications): rows are groups of notification
// kinds, columns are what a notification does on arrival. Every decision about whether a
// notification raises the unread dot, an OS notification or a spoken announcement reads its cell
// through notifyCell, so the table and the delivery code cannot disagree.
//
// Storage is resolved at read time, never migrated on write: a cell is its explicit override, else
// the legacy switch that governed it before the table (childIdleNotify, usageResetNotify), else
// ON. Seeding the overrides from the legacy keys at load would read DEFAULTS on a fresh device
// before the ui-prefs hydrate delivers the real childIdleNotify, and turn child notifications back
// on for someone who had them off.
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { Settings } from "../../lib/settings.ts";

export type NotifyRow =
  | "turn-own"
  | "turn-child"
  | "needs-input"
  | "usage-reset"
  | "session-report"
  | "rate-limit"
  | "schedule"
  | "handoff"
  | "cloud-login"
  | "terminal"
  | "other";

export type NotifyEffect = "unread" | "os" | "voice";

export interface NotifyRowDef {
  row: NotifyRow;
  label: MsgKey;
  hint: MsgKey;
  // The unread dot cannot be muted: the row asks a person to act, and a notification marked read
  // on arrival is one nobody answers.
  unreadFixed?: boolean;
}

export const NOTIFY_ROWS: NotifyRowDef[] = [
  { row: "turn-own", label: "noti.row_turn_own", hint: "noti.row_turn_own_hint" },
  { row: "turn-child", label: "noti.row_turn_child", hint: "noti.row_turn_child_hint" },
  { row: "needs-input", label: "noti.row_needs_input", hint: "noti.row_needs_input_hint", unreadFixed: true },
  { row: "usage-reset", label: "noti.row_usage_reset", hint: "noti.row_usage_reset_hint" },
  { row: "session-report", label: "noti.row_session_report", hint: "noti.row_session_report_hint" },
  { row: "rate-limit", label: "noti.row_rate_limit", hint: "noti.row_rate_limit_hint" },
  { row: "schedule", label: "noti.row_schedule", hint: "noti.row_schedule_hint" },
  { row: "handoff", label: "noti.row_handoff", hint: "noti.row_handoff_hint", unreadFixed: true },
  { row: "cloud-login", label: "noti.row_cloud_login", hint: "noti.row_cloud_login_hint", unreadFixed: true },
  { row: "terminal", label: "noti.row_terminal", hint: "noti.row_terminal_hint" },
  { row: "other", label: "noti.row_other", hint: "noti.row_other_hint" },
];

const KIND_ROW: Record<string, NotifyRow> = {
  question: "needs-input",
  "plan-approval": "needs-input",
  "permission-request": "needs-input",
  "carried-interaction": "needs-input",
  "usage-reset": "usage-reset",
  "session-report": "session-report",
  "rate-limit-reached": "rate-limit",
  "rate-limit-resumed": "rate-limit",
  "schedule-failed": "schedule",
  "schedule-skipped": "schedule",
  "schedule-result": "schedule",
  "handoff-offer": "handoff",
  "handoff-accepted": "handoff",
  "handoff-expired": "handoff",
  "aws-login-required": "cloud-login",
  "aws-sso-expiring": "cloud-login",
  "gcp-login-required": "cloud-login",
  "terminal-notification": "terminal",
};

/** The row a notification kind belongs to. A kind this Console does not know (a newer CP) lands
 *  in "other", whose cells default ON — when in doubt, notify. `child` is whether the target is a
 *  session another session spawned; it only splits answer-ready. */
export function notifyRowOf(kind: string, child: boolean): NotifyRow {
  if (kind === "answer-ready") return child ? "turn-child" : "turn-own";
  return KIND_ROW[kind] ?? "other";
}

type CellSettings = Pick<Settings, "notifyUnread" | "notifyDevice" | "childIdleNotify" | "usageResetNotify">;

const unreadFixed = (row: NotifyRow) => !!NOTIFY_ROWS.find((r) => r.row === row)?.unreadFixed;

// A map read from localStorage, the server or an imported bundle can hold anything; a cell takes
// only a boolean and falls through otherwise.
function override(map: unknown, key: string): boolean | undefined {
  if (!map || typeof map !== "object" || Array.isArray(map)) return undefined;
  const v = (map as Record<string, unknown>)[key];
  return typeof v === "boolean" ? v : undefined;
}

// legacy is the switch that governed the cell before the table. The child row's dot is stored in
// childIdleNotify itself, so an older Console still reads it.
function legacy(s: CellSettings, row: NotifyRow): boolean {
  if (row === "turn-child") return s.childIdleNotify !== false;
  if (row === "usage-reset") return s.usageResetNotify !== false;
  return true;
}

/** Whether the cell is on. The read-aloud cell is further gated by its column master
 *  (ttsSessionNotify, or ttsEnabled for usage-reset) at the delivery site. */
export function notifyCell(s: CellSettings, row: NotifyRow, effect: NotifyEffect): boolean {
  if (effect === "unread") {
    if (unreadFixed(row)) return true;
    if (row === "turn-child") return s.childIdleNotify !== false;
    return override(s.notifyUnread, row) ?? true;
  }
  return override(s.notifyDevice, `${row}.${effect}`) ?? legacy(s, row);
}

function cleanMap(map: unknown): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  if (!map || typeof map !== "object" || Array.isArray(map)) return out;
  for (const [k, v] of Object.entries(map as Record<string, unknown>)) if (typeof v === "boolean") out[k] = v;
  return out;
}

/** The settings patch that turns one cell on or off, leaving every other cell where it reads now.
 *  The legacy keys stay written so an older Console tab behaves the same: the child row's dot IS
 *  childIdleNotify, and usageResetNotify follows "either usage cell is on" (an older tab gates
 *  both by it). Siblings that still fall back to a legacy key are pinned first, or flipping that
 *  key would flip them too. */
export function notifyCellPatch(s: CellSettings, row: NotifyRow, effect: NotifyEffect, on: boolean): Partial<Settings> {
  if (effect === "unread") {
    if (unreadFixed(row)) return {};
    if (row === "turn-child") {
      const device = cleanMap(s.notifyDevice);
      for (const e of ["os", "voice"] as const) device[`${row}.${e}`] = notifyCell(s, row, e);
      return { childIdleNotify: on, notifyDevice: device };
    }
    const unread = cleanMap(s.notifyUnread);
    if (on) delete unread[row];
    else unread[row] = false;
    return { notifyUnread: unread };
  }
  const device = cleanMap(s.notifyDevice);
  if (row === "turn-child" || row === "usage-reset") {
    for (const e of ["os", "voice"] as const) device[`${row}.${e}`] = notifyCell(s, row, e);
  }
  device[`${row}.${effect}`] = on;
  const patch: Partial<Settings> = { notifyDevice: device };
  if (row === "usage-reset") patch.usageResetNotify = device["usage-reset.os"] || device["usage-reset.voice"];
  return patch;
}
