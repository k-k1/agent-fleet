// The member roster's pure logic: which state a card announces, whether it needs the admin,
// and the filter / sort / search over GET …/members. Kept out of the component so the rules
// can be tested in the node project without mounting the roster.
//
// Everything here reads what the Control Plane sent; nothing derives idle or auto-stop on its
// own (the MemberIdleChip rule). The one clock comparison, a "working" hold past its lapseAt,
// only asks whether the reaper's own deadline has gone by.
import { nextHomeDiskPressure } from "../../../core/store/homeDiskPressure.ts";
import type { Member } from "../parts/adminShared.ts";

/** What the card's state badge says. One word per value, never colour alone (WCAG 1.4.1). */
export type RosterState = "running" | "starting" | "stopped" | "never" | "auto_stopped" | "removed";

export function rosterState(m: Member): RosterState {
  const alive = m.state === "running" || m.state === "starting";
  // A removed member's workspace can still be up, and stopping it is the next offboarding step,
  // so a live workspace keeps its live state; the role chip already says "removed".
  if (alive) return m.state as RosterState;
  if (m.status === "removed") return "removed";
  if (m.auto_stop) return "auto_stopped";
  // "none" is also what ecs-ec2 reports after Clean home and native when stopped, so only a
  // CP that says there is no row at all earns "never started". An older CP omits
  // has_workspace; claiming "never" there would be a guess.
  if (m.state === "none" && m.has_workspace === false) return "never";
  return "stopped";
}

export type AttentionReason = "auto_stop" | "hold_lapsed" | "disk_warn" | "disk_error" | "boot_install";

const GIB = 1024 ** 3;
/** The thresholds #2383 uses for the boot-time disk check: warn at 85 %, error at 95 % or with
 *  less than 2 GiB left. */
export const DISK_WARN_RATIO = 0.85;
export const DISK_ERROR_RATIO = 0.95;
export const DISK_ERROR_FREE = 2 * GIB;

export type DiskLevel = "ok" | "warn" | "error";

/** The gauge's colour class, null when there is nothing to judge against (no reading, or a
 *  reading with no total such as a local deployment with no disk quota). */
export function diskLevel(m: Member): DiskLevel | null {
  const d = m.disk;
  if (!d || !d.total || d.total <= 0 || !Number.isFinite(d.used)) return null;
  const ratio = d.used / d.total;
  if (ratio >= DISK_ERROR_RATIO || d.total - d.used < DISK_ERROR_FREE) return "error";
  if (ratio >= DISK_WARN_RATIO) return "warn";
  return "ok";
}

export function diskRatio(m: Member): number | null {
  const d = m.disk;
  return d && d.total && d.total > 0 ? d.used / d.total : null;
}

/** The CLIs that failed to install at the last start, as [tool, reason] sorted by tool; empty
 *  when none failed or the Agent did not say. */
export function bootFailures(failed?: Record<string, string> | null): [string, string][] {
  return Object.entries(failed ?? {}).sort(([a], [b]) => a.localeCompare(b));
}

/** Whether the administrator's Force-stop is offered, from the stats poll: a running workspace,
 *  or one the CP reports as starting. A start that never converges has no other exit, so
 *  "starting" must not grey the button out. An unread poll (null) offers nothing. */
export function memberStopAvailable(stats: { running?: boolean; starting?: boolean } | null | undefined): boolean {
  return !!stats && (stats.running === true || stats.starting === true);
}

/** The member detail's "home is full" flag after one stats poll. The numbers are the ones the
 *  member's own notice uses (homeDiskPressure.ts, hysteresis included), and it is raised only
 *  where home_own says disk_used / disk_total describe this member's own volume: elsewhere they
 *  are a whole host or a shared filesystem and the member is not the one who filled it. A
 *  stopped or unreadable workspace carries no reading and changes nothing. */
export function nextMemberHomePressure(
  prev: boolean,
  s: { home_own?: boolean; disk_used?: number; disk_total?: number } | null | undefined,
): boolean {
  if (!s?.home_own) return false;
  if (s.disk_used == null || s.disk_total == null) return prev;
  return nextHomeDiskPressure(prev, s.disk_used, s.disk_total);
}

/** The numbers the home-full notice prints, only when the current poll carries both: the flag
 *  keeps its state across a poll without a reading, but a notice with no figures to show must not
 *  print "NaN GiB". */
export function homeFullFigures(
  s: { home_own?: boolean; disk_used?: number; disk_total?: number } | null | undefined,
): { used: number; total: number } | null {
  const used = s?.disk_used;
  const total = s?.disk_total;
  if (typeof used !== "number" || typeof total !== "number" || !Number.isFinite(used) || !(total > 0)) return null;
  return { used, total };
}

/** Why this member needs the admin, empty when nothing does. */
export function attentionReasons(m: Member, now = Date.now()): AttentionReason[] {
  const out: AttentionReason[] = [];
  if (m.auto_stop) out.push("auto_stop");
  if (m.state === "running" && m.idle) {
    const lapsed = (m.idle.holders ?? []).some((h) => {
      if (h.kind !== "working" || !h.lapseAt) return false;
      const t = Date.parse(h.lapseAt);
      return Number.isFinite(t) && t < now;
    });
    if (lapsed) out.push("hold_lapsed");
  }
  const dl = diskLevel(m);
  if (dl === "error") out.push("disk_error");
  else if (dl === "warn") out.push("disk_warn");
  if (bootFailures(m.boot_failed).length > 0) out.push("boot_install");
  return out;
}

export type RosterFilter = "all" | "running" | "attention" | "stopped" | "removed";
export type RosterSort = "attention" | "name" | "last_active" | "disk";

export const ROSTER_FILTERS: RosterFilter[] = ["all", "running", "attention", "stopped", "removed"];

/** The card's headline: the e-mail when there is one (people recognise it), else user_key. */
export function memberTitle(m: Member): string {
  return m.email || m.user_key;
}

export function matchesFilter(m: Member, f: RosterFilter, now = Date.now()): boolean {
  switch (f) {
    case "all":
      return true;
    case "running":
      return m.state === "running" || m.state === "starting";
    case "attention":
      return attentionReasons(m, now).length > 0;
    case "stopped": {
      const s = rosterState(m);
      return m.status !== "removed" && (s === "stopped" || s === "never" || s === "auto_stopped");
    }
    case "removed":
      return m.status === "removed";
  }
}

export function matchesQuery(m: Member, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return (m.email || "").toLowerCase().includes(needle) || m.user_key.toLowerCase().includes(needle);
}

const byName = (a: Member, b: Member) =>
  memberTitle(a).localeCompare(memberTitle(b)) || a.user_key.localeCompare(b.user_key);

/** last_active_at as epoch ms, null when absent or unparsable. */
export function lastActiveMs(m: Member): number | null {
  if (!m.last_active_at) return null;
  const t = Date.parse(m.last_active_at);
  return Number.isFinite(t) ? t : null;
}

export function sortMembers(list: Member[], sort: RosterSort, now = Date.now()): Member[] {
  const out = [...list];
  if (sort === "name") return out.sort(byName);
  if (sort === "disk") {
    // Fullest first: error before warn before ok, then by ratio. A member with no ratio (never
    // measured, or no total) goes last — a missing figure is not an empty disk.
    const rank = (m: Member) => ({ error: 0, warn: 1, ok: 2 })[diskLevel(m) ?? "ok"];
    return out.sort((a, b) => {
      const ra = diskRatio(a);
      const rb = diskRatio(b);
      if (ra === null && rb === null) return byName(a, b);
      if (ra === null) return 1;
      if (rb === null) return -1;
      return rank(a) - rank(b) || rb - ra || byName(a, b);
    });
  }
  if (sort === "last_active") {
    // Most recent first; a member with no value goes last rather than first, where a missing
    // date would read as "never active" at the top of the list.
    return out.sort((a, b) => {
      const ta = lastActiveMs(a);
      const tb = lastActiveMs(b);
      if (ta === null && tb === null) return byName(a, b);
      if (ta === null) return 1;
      if (tb === null) return -1;
      return tb - ta || byName(a, b);
    });
  }
  // A full disk first (the acceptance rule: a member at >= 95 % leads the default order), then
  // every other reason, then the rest.
  const rank = (m: Member) => {
    const r = attentionReasons(m, now);
    return r.includes("disk_error") ? 0 : r.length > 0 ? 1 : 2;
  };
  return out.sort((a, b) => rank(a) - rank(b) || byName(a, b));
}

export interface RosterCounts {
  total: number;
  /** Running and starting together, the same set as the "running" filter: a summary that
   *  counted otherwise would disagree with the filter button beside it. */
  running: number;
  attention: number;
  byFilter: Record<RosterFilter, number>;
}

export function rosterCounts(list: Member[], now = Date.now()): RosterCounts {
  const byFilter = Object.fromEntries(
    ROSTER_FILTERS.map((f) => [f, list.filter((m) => matchesFilter(m, f, now)).length]),
  ) as Record<RosterFilter, number>;
  return {
    total: list.length,
    running: byFilter.running,
    attention: byFilter.attention,
    byFilter,
  };
}
