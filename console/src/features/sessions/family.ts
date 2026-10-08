// Parent/child lineage between sessions (Session.originSession), shared by the sessions
// overview (features/overview) and the command palette. Pure — no store, no clock.
//
// A parent that is not among the sessions given is no parent here: its child is a root, so
// an archived or deleted parent never hides a row. `seen` guards a corrupted originSession
// cycle, which would otherwise recurse forever (the same hazard lib/project.ts's
// sessionLineages guards).
import { compareText } from "../../lib/intl.ts";
import type { Session } from "../../types/session.ts";

/** A session and how deep it sits under its family's root (0 = the root itself). */
export interface FamilyRow {
  session: Session;
  depth: number;
}

/** Children by parent name, only for parents that are in `sessions`. Also returns the
 *  roots (no parent present, or a self-reference) in input order. */
export function childrenIndex(sessions: Session[]): { childrenOf: Map<string, Session[]>; roots: Session[] } {
  const present = new Set(sessions.map((s) => s.name));
  const childrenOf = new Map<string, Session[]>();
  const roots: Session[] = [];
  for (const s of sessions) {
    const parent = s.originSession && present.has(s.originSession) && s.originSession !== s.name ? s.originSession : "";
    if (parent) childrenOf.set(parent, [...(childrenOf.get(parent) || []), s]);
    else roots.push(s);
  }
  return { childrenOf, roots };
}

/** One family, parent first, then each child's subtree in spawn order.
 *
 * Siblings oldest first: inside one family the order IS the spawn order, so a new child
 * appends at the end instead of pushing its elders down. */
export function familyRows(
  root: Session,
  childrenOf: Map<string, Session[]>,
  seen: Set<string>,
  depth = 0,
): FamilyRow[] {
  if (seen.has(root.name)) return [];
  seen.add(root.name);
  const out: FamilyRow[] = [{ session: root, depth }];
  const kids = [...(childrenOf.get(root.name) || [])].sort(
    (a, b) => compareText(a.createdAt || "", b.createdAt || "") || compareText(a.name, b.name),
  );
  for (const k of kids) out.push(...familyRows(k, childrenOf, seen, depth + 1));
  return out;
}

/** Lay an already-ordered list out as family blocks. Each family is one contiguous block
 *  (parent first, descendants in spawn order) placed where its earliest member sits in
 *  `ordered`, so the primary order (attention) decides where a family goes and the family
 *  only decides what travels with it. Every input row appears exactly once, cycles included.
 *
 *  `groupable` limits which sessions may join a family: the rest stay roots in place (the
 *  palette uses it to keep a session born while open at the foot). */
export function layoutFamilies(ordered: Session[], groupable: (s: Session) => boolean = () => true): FamilyRow[] {
  const pool = ordered.filter(groupable);
  const { childrenOf, roots } = childrenIndex(pool);
  const seen = new Set<string>();
  const blocks: FamilyRow[][] = roots.map((r) => familyRows(r, childrenOf, seen));
  // A cycle leaves its members unreachable from any root; each becomes its own block.
  for (const s of pool) if (!seen.has(s.name)) blocks.push(familyRows(s, childrenOf, seen));
  const pos = new Map(ordered.map((s, i) => [s.name, i]));
  const at = (b: FamilyRow[]) => Math.min(...b.map((r) => pos.get(r.session.name) ?? Infinity));
  const placed = blocks.filter((b) => b.length).map((b) => ({ b, at: at(b) }));
  for (const s of ordered) if (!groupable(s)) placed.push({ b: [{ session: s, depth: 0 }], at: pos.get(s.name)! });
  return placed.sort((x, y) => x.at - y.at).flatMap((x) => x.b);
}
