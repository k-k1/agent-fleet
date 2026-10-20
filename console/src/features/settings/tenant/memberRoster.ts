// The member roster's pure logic: which state a card announces, whether it needs the admin,
// and the filter / sort / search over GET …/members. Kept out of the component so the rules
// can be tested in the node project without mounting the roster.
//
// Everything here reads what the Control Plane sent; nothing derives idle or auto-stop on its
// own (the MemberIdleChip rule). The one clock comparison, a "working" hold past its lapseAt,
// only asks whether the reaper's own deadline has gone by.
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

export type AttentionReason = "auto_stop" | "hold_lapsed";

/** Why this member needs the admin, empty when nothing does. Disk and boot-install reasons
 *  arrive with the data that carries them (#2385 phase 2). */
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
  return out;
}

export type RosterFilter = "all" | "running" | "attention" | "stopped" | "removed";
export type RosterSort = "attention" | "name" | "last_active";

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
  const rank = (m: Member) => (attentionReasons(m, now).length > 0 ? 0 : 1);
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
