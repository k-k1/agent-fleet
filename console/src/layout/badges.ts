// Pane ordinals + rail badge derivations — port of lib/panebadge.ts onto the new
// layout types (pane.session + content union). An ordinal is the pane's 1-based
// visual reading order (columns left→right; within a column, top→bottom); its
// color class cycles a fixed 8-hue palette so the same pane reads identically as
// a corner chip, a list badge, and a mini-map cell. Pure; vitest-friendly.
import type { Cell, Layout, View } from "./types.ts";
import { selectedView } from "./ops.ts";

export const PANE_ORD_COUNT = 8;

export interface PaneRow {
  id: string;
  ordinal: number;
  col: number;
  row: number;
  cell: Cell;
  pane?: View;
}

export function ordClass(ordinal: number): string {
  const n = ((ordinal - 1) % PANE_ORD_COUNT) + 1;
  return "pane-ord-" + n;
}

export function paneRows(layout: Layout | null | undefined): PaneRow[] {
  const rows: PaneRow[] = [];
  const cols = layout?.cols ?? [];
  let ord = 0;
  for (let ci = 0; ci < cols.length; ci++) {
    const col = cols[ci];
    for (let ri = 0; ri < col.cells.length; ri++) {
      ord += 1;
      const cell = col.cells[ri];
      rows.push({ id: cell.id, ordinal: ord, col: ci, row: ri, cell, pane: selectedView(cell) });
    }
  }
  return rows;
}

export function paneOrdinals(layout: Layout | null | undefined): Map<string, number> {
  const m = new Map<string, number>();
  for (const r of paneRows(layout)) m.set(r.id, r.ordinal);
  return m;
}

type Ref = { ordinal: number; id: string };
const push = (m: Map<string, Ref[]>, key: string, r: PaneRow) => {
  const arr = m.get(key) || [];
  arr.push({ ordinal: r.ordinal, id: r.id });
  m.set(key, arr);
};

/** session name → panes showing it (terminal view), in visual order. */
export function sessionPanes(layout: Layout | null | undefined): Map<string, Ref[]> {
  const m = new Map<string, Ref[]>();
  for (const r of paneRows(layout)) {
    if (r.pane?.content.kind === "terminal" && r.pane.session) push(m, r.pane.session, r);
  }
  return m;
}

/** repo name → panes showing its SCM surfaces (graph/changes/commit). */
export function repoPanes(layout: Layout | null | undefined): Map<string, Ref[]> {
  const m = new Map<string, Ref[]>();
  for (const r of paneRows(layout)) {
    const c = r.pane?.content;
    if (!c) continue;
    if ((c.kind === "scm" || c.kind === "changes" || c.kind === "commit") && c.scmRepo)
      push(m, c.scmRepo, r);
  }
  return m;
}

/** assistant-chat conversation id → panes showing it. */
export function chatPanes(layout: Layout | null | undefined): Map<string, Ref[]> {
  const m = new Map<string, Ref[]>();
  for (const r of paneRows(layout)) {
    const c = r.pane?.content;
    if (!c) continue;
    if (c.kind === "chat" && c.conversationId) push(m, c.conversationId, r);
  }
  return m;
}

/** Conversation shown in the ACTIVE pane — the rail marks that row as current, the
 * chat twin of `activePane(l)?.session` for session rows. null when the active pane
 * isn't a chat, or holds a draft that hasn't become a conversation yet. */
export function activeChatId(layout: Layout | null | undefined): string | null {
  for (const r of paneRows(layout)) {
    if (r.id !== layout?.activeCellId) continue;
    return r.pane?.content.kind === "chat" ? r.pane.content.conversationId : null;
  }
  return null;
}

export function paneCount(layout: Layout | null | undefined): number {
  let n = 0;
  for (const c of layout?.cols ?? []) n += c.cells.length;
  return n;
}
