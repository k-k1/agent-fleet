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

/** The opened parents, and the toggle. `known` is every session name of this workspace right
 *  now: names that are gone are dropped on the way, so the stored list never outgrows the
 *  sessions. */
export function useActiveExpanded(): [string[], (name: string, known: ReadonlySet<string>) => void] {
  const [raw, setRaw] = useFoldValue(ACTIVE_EXPANDED_KEY);
  const expanded = useMemo(() => parse(raw), [raw]);
  const toggle = (name: string, known: ReadonlySet<string>) => {
    const cur = expanded.filter((n) => known.has(n));
    setRaw(JSON.stringify(cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));
  };
  return [expanded, toggle];
}
