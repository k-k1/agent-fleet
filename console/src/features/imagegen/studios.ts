// studios — what the studio picker calls a studio. Pure, for the node test project.
//
// A studio the member has not named is told apart by when it was made ("Studio 9/27 14:05"),
// not by an id fragment: two "Untitled 48c71fb2" rows say nothing a person can recognise.
import { zonedParts } from "../../lib/intl.ts";
import type { StudioSummary } from "./wire.ts";

/** The picker's value for "＋ new studio": never a studio id (those are UUIDs). */
export const NEW_STUDIO = "__new__";

/** The Agent's own cap on the title (`studioShortTextMax`, studio_validate.go). */
export const STUDIO_TITLE_MAX = 200;

/** `M/D HH:MM` in the member's time zone, or "" for a missing or unreadable stamp. */
export function studioStamp(iso: string | undefined): string {
  if (!iso) return "";
  const p = zonedParts(iso);
  if (!p) return "";
  const hh = String(p.hour).padStart(2, "0");
  const mm = String(p.minute).padStart(2, "0");
  return `${p.month}/${p.day} ${hh}:${mm}`;
}

/**
 * The name a studio shows: its title, else the dated fallback `dated(stamp)` phrases in the
 * member's language. A summary from an older Agent has no `created_at`; the last change is the
 * closest stamp it has.
 */
export function studioName(
  s: Pick<StudioSummary, "title" | "id" | "created_at" | "updated_at">,
  dated: (stamp: string) => string,
): string {
  const title = (s.title || "").trim();
  if (title) return title;
  return dated(studioStamp(s.created_at || s.updated_at) || s.id.slice(0, 8));
}
