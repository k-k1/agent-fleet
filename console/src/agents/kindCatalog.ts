// The Console's view of the Agent's kind catalog (GET /api/agents/kinds, #1077/#2279).
//
// The catalog owns the facts about a kind that the Agent also enforces: whether it has any
// Terminal (CLI) route (managedOnly), which driver a caller that cannot drive a pane should
// ask for (managedDefault), and whether it takes MCP servers (mcpClient). Colour, icon and
// wording stay with the descriptors in registry.ts.
//
// Safe by construction: every read answers from FALLBACK_KIND_FACTS until a catalog has
// arrived, and again if it never does, so the session list and the launch dialogs render
// whether the request is slow, fails or returns nothing usable. The fallback is a copy, so
// kindCatalog.test.ts holds it equal to control-plane/internal/agentkinds/catalog.json —
// the build-time snapshot of the same answer — and fails when either side drifts.
import { useSyncExternalStore } from "react";
import type { SessionKind } from "../types/session.ts";

export type Tri = "yes" | "no" | "unverified";

export interface KindFacts {
  managedOnly: boolean;
  managedDefault: boolean;
  mcpClient: Tri;
}

const f = (managedOnly: boolean, managedDefault: boolean, mcpClient: Tri): KindFacts => ({
  managedOnly,
  managedDefault: managedOnly || managedDefault,
  mcpClient,
});

// The order of this table is not significant; SESSION_KINDS owns display order.
export const FALLBACK_KIND_FACTS: Record<SessionKind, KindFacts> = {
  claude: f(false, false, "yes"),
  codex: f(false, true, "yes"),
  cursor: f(false, true, "yes"),
  copilot: f(false, true, "yes"),
  kiro: f(false, true, "yes"),
  agy: f(false, false, "yes"),
  opencode: f(false, true, "yes"),
  lcpp: f(true, true, "yes"),
  muse: f(true, true, "yes"),
  shell: f(false, false, "no"),
  ssm: f(false, false, "no"),
};

let live: ReadonlyMap<string, KindFacts> | null = null;
let version = 0;
const subs = new Set<() => void>();

function publish(next: ReadonlyMap<string, KindFacts> | null): void {
  live = next;
  version++;
  for (const fn of subs) fn();
}

/** Reads the response defensively: a malformed entry is dropped, never thrown on. An answer
 *  with no usable entry is null, which callers treat as "no catalog". */
export function parseKindCatalog(body: unknown): Map<string, KindFacts> | null {
  const list = (body as { kinds?: unknown } | null)?.kinds;
  if (!Array.isArray(list)) return null;
  const out = new Map<string, KindFacts>();
  for (const e of list) {
    // The Go wire requires all three; a row that lacks one is dropped rather than read as
    // "false", so the kind keeps its fallback facts.
    if (!e || typeof e !== "object" || typeof e.kind !== "string" || !e.kind) continue;
    if (typeof e.managedOnly !== "boolean" || typeof e.managedDefault !== "boolean") continue;
    if (e.mcpClient !== "yes" && e.mcpClient !== "no" && e.mcpClient !== "unverified") continue;
    out.set(e.kind, {
      managedOnly: e.managedOnly,
      managedDefault: e.managedOnly || e.managedDefault,
      mcpClient: e.mcpClient,
    });
  }
  return out.size ? out : null;
}

/** The facts for an exact kind id, or undefined for a spelling the Agent has no kind of.
 *  Alternate spellings ("Claude", "claude-code") deliberately find nothing: they were never
 *  kinds, and resolving them here would let the Console accept what the Agent refuses. */
export function kindFacts(kind: string): KindFacts | undefined {
  return live?.get(kind) ?? FALLBACK_KIND_FACTS[kind as SessionKind];
}

/** True when the loaded catalog names this kind. False before the first answer. */
export function catalogHasKind(kind: string): boolean {
  return !!live?.has(kind);
}

/** The loaded catalog's kinds in the Agent's order, [] before the first answer. */
export function catalogKinds(): string[] {
  return live ? [...live.keys()] : [];
}

export function subscribeKindCatalog(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

export function kindCatalogVersion(): number {
  return version;
}

/** Re-renders the caller when a catalog arrives or is dropped. Components that read a fact
 *  at render time and can be on screen before the answer lands call this once. */
export function useKindCatalogVersion(): number {
  return useSyncExternalStore(subscribeKindCatalog, kindCatalogVersion);
}

/** Loader hooks: install a parsed catalog, or drop it (null). */
export function setKindCatalog(next: ReadonlyMap<string, KindFacts> | null): void {
  if (next === null && live === null) return;
  publish(next);
}

export function hasKindCatalog(): boolean {
  return live !== null;
}
