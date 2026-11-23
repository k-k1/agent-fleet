// Which icons the activity bar shows (ADR 0142 decision 11). The member turns a view's icon off in
// the bar's context menu; the choice is the `railViewsOff` setting. Kept apart from the component so
// each rule can be tested without a DOM.
import { RAIL_VIEWS } from "../../core/store/leftRail.ts";
import type { RailView } from "../../core/store/leftRail.ts";
import type { RailBadge } from "./badges.ts";

/** The icons to show: every usable view whose icon is not off, plus the view on screen (null while
 * the left pane is folded away: nothing is on screen then, and any icon may go). The view on screen
 * stays whatever the stored choice says (it may have been turned off on another browser), so the
 * pane the member is reading never loses its icon. Never empty while any view is usable. */
export function shownViews(usable: readonly RailView[], off: readonly string[], onScreen: RailView | null): RailView[] {
  const shown = usable.filter((v) => v === onScreen || !off.includes(v));
  return shown.length > 0 ? shown : usable.slice(0, 1);
}

/** Why a listed view cannot be switched, or null when it can. `current` is the view on screen and
 * `last` the only icon left; `unavailable` is a view this deployment cannot use. */
export type LockReason = "current" | "last" | "unavailable";

export interface MenuEntry {
  id: RailView;
  checked: boolean;
  lock: LockReason | null;
}

/** One entry per view the bar knows, in bar order. An unavailable view is never checked: it has no
 * icon whatever the stored choice says. Only an icon that is shown can be locked on. Turning an
 * icon back on is always allowed. */
export function menuEntries(all: readonly RailView[], usable: readonly RailView[], off: readonly string[], onScreen: RailView | null): MenuEntry[] {
  const shown = shownViews(usable, off, onScreen);
  return all.map((id) => {
    if (!usable.includes(id)) return { id, checked: false, lock: "unavailable" };
    const checked = shown.includes(id);
    const lock: LockReason | null = !checked ? null : id === onScreen ? "current" : shown.length === 1 ? "last" : null;
    return { id, checked, lock };
  });
}

const known = (id: string): boolean => (RAIL_VIEWS as readonly string[]).includes(id);

/** The off list after Reset: only the ids this build knows are cleared, so a newer build's choice
 * (a view this build has no icon for) survives a reset made here. */
export function resetOff(off: readonly string[]): string[] {
  return off.filter((id) => !known(id));
}

/** Is there anything for Reset to clear? */
export const canResetOff = (off: readonly string[]): boolean => off.some(known);

/** The off list after switching `id`. Ids the build does not know stay, so a newer build's choice
 * survives. */
export function toggleOff(off: readonly string[], id: RailView, on: boolean): string[] {
  const rest = off.filter((v) => v !== id);
  return on ? rest : [...rest, id];
}

/** The views without an icon that have something waiting on the member (ADR 0142 open question 3,
 * decided: a waiting badge is shown, news is not). A waiting badge stays until the member acts, so
 * leaving it unseen would leave a session stopped on an answer with nothing on the bar to say so.
 * News clears by looking, and a view the member turned off is one they chose not to look at. */
export function hiddenWaiting(
  usable: readonly RailView[],
  shown: readonly RailView[],
  badges: Partial<Record<RailView, RailBadge>>,
): { id: RailView; badge: RailBadge }[] {
  const out: { id: RailView; badge: RailBadge }[] = [];
  for (const id of usable) {
    const badge = badges[id];
    if (badge?.tone === "waiting" && !shown.includes(id)) out.push({ id, badge });
  }
  return out;
}
