// Which parents the member opened in the Active view (ADR 0145). Kept like the left pane's
// other folds (foldStore): per browser, and per signed-in member and tenant, because a session
// name means nothing in another workspace.
import { useMemo } from "react";
import { useFoldValue } from "../../lib/useFold.ts";

export const ACTIVE_EXPANDED_KEY = "af-active-expanded";

const parse = (raw: string | null): string[] => {
  try {
    const v: unknown = JSON.parse(raw || "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
};

/** How many opened parents are remembered. Names of sessions that are gone are never
 *  looked for: the session list on screen can still be the previous tenant's for a moment
 *  after a switch, and a list pruned against it would lose this tenant's choices. A session
 *  name is not reused, so a stale entry opens nothing; the cap alone bounds the list. */
export const ACTIVE_EXPANDED_MAX = 200;

/** The opened parents, and the toggle. */
export function useActiveExpanded(): [string[], (name: string) => void] {
  const [raw, setRaw] = useFoldValue(ACTIVE_EXPANDED_KEY);
  const expanded = useMemo(() => parse(raw), [raw]);
  const toggle = (name: string) => {
    const next = expanded.includes(name) ? expanded.filter((n) => n !== name) : [...expanded, name];
    setRaw(JSON.stringify(next.slice(-ACTIVE_EXPANDED_MAX)));
  };
  return [expanded, toggle];
}
