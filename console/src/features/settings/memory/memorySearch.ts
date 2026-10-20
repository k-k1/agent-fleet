// What Settings > Agent memory > Memories and the command palette's memory mode share: how a
// memory is keyed, which listed memories a query matches, and how the palette names one memory
// to the Settings dialog.
import type { MemoryListed, MemorySearchHit } from "./memoryTypes.ts";

/** "user" or "project:<id>": one browsable scope. */
export const scopeKey = (m: { scope: string; project?: { id: string } }) =>
  m.scope === "user" ? "user" : "project:" + (m.project?.id ?? "");
export const keyOf = (m: { scope: string; project?: { id: string }; name: string }) => scopeKey(m) + "/" + m.name;

/**
 * The memories of `rows` that `q` (already trimmed and lower-cased; empty = all) finds: first the
 * ones whose listed fields match, as listed, then those only the body search found, in its rank
 * order. A hit that is not among `rows` is dropped, so a scope or a withheld memory never leaks in
 * through the search side.
 */
export function matchMemories(rows: MemoryListed[], hits: MemorySearchHit[], q: string): MemoryListed[] {
  if (!q) return rows;
  const byField = (m: MemoryListed) =>
    `${m.name}\n${m.description}\n${m.type ?? ""}\n${m.authorKind}\n${m.authorSession}`.toLowerCase().includes(q);
  const fieldRows = rows.filter(byField);
  const seen = new Set(fieldRows.map(keyOf));
  const byKey = new Map(rows.map((m) => [keyOf(m), m]));
  const bodyRows = hits.flatMap((h) => {
    const m = byKey.get(keyOf(h));
    if (!m || seen.has(keyOf(h))) return [];
    seen.add(keyOf(h));
    return [m];
  });
  return [...fieldRows, ...bodyRows];
}

/** One memory, named for `openSettings("memory:memories", { target })`. */
export interface MemoryTarget {
  scope: "user" | "project";
  /** Project id; "" for the user scope. */
  project: string;
  name: string;
}
const TARGET_PREFIX = "memory-entry:";
export const memoryTarget = (m: { scope: string; project?: { id: string }; name: string }): string =>
  TARGET_PREFIX +
  JSON.stringify({
    scope: m.scope === "user" ? "user" : "project",
    project: m.scope === "user" ? "" : (m.project?.id ?? ""),
    name: m.name,
  } satisfies MemoryTarget);
/** The memory a settings target names; null when it names something else (an agent kind, say). */
export function parseMemoryTarget(target: string | null): MemoryTarget | null {
  if (!target || !target.startsWith(TARGET_PREFIX)) return null;
  try {
    const t = JSON.parse(target.slice(TARGET_PREFIX.length));
    const ok = (t?.scope === "user" || t?.scope === "project") && typeof t.project === "string" && typeof t.name === "string";
    return ok ? (t as MemoryTarget) : null;
  } catch {
    return null;
  }
}
