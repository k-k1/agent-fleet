// Pane ordinals + colors: the shared identity that ties a session-list row, the
// layout mini-map, and the on-screen pane together. An ordinal is the pane's
// 1-based visual reading order (columns left→right; within a column, panes
// top→bottom). Color cycles a fixed 8-hue palette (.pane-ord-1..8 in styles.css),
// so the same pane reads identically as a corner chip, a list badge, and a map cell.

export const PANE_ORD_COUNT = 8;

// ordClass maps a 1-based ordinal to its color class (cycling past 8, though the
// layout caps at 8 panes so cycling is only a safety net).
export function ordClass(ordinal) {
  const n = ((ordinal - 1) % PANE_ORD_COUNT) + 1;
  return "pane-ord-" + n;
}

// paneRows flattens a layout into visual order, tagging each pane with its ordinal
// and grid position (col/row indices). One pass, reused by every consumer.
export function paneRows(layout) {
  const rows = [];
  let ord = 0;
  for (let ci = 0; ci < (layout?.cols?.length || 0); ci++) {
    const col = layout.cols[ci];
    for (let ri = 0; ri < col.panes.length; ri++) {
      ord += 1;
      rows.push({ id: col.panes[ri].id, ordinal: ord, col: ci, row: ri, pane: col.panes[ri] });
    }
  }
  return rows;
}

// paneOrdinals maps pane id → ordinal.
export function paneOrdinals(layout) {
  const m = new Map();
  for (const r of paneRows(layout)) m.set(r.id, r.ordinal);
  return m;
}

// sessionPanes maps a session name → the panes currently showing it, as
// [{ ordinal, id }] in visual order. Usually one entry, but the same session can
// be attached in two panes. Only terminal panes carry a session.
export function sessionPanes(layout) {
  const m = new Map();
  for (const r of paneRows(layout)) {
    const p = r.pane;
    if (p.kind !== "terminal" || !p.session) continue;
    const arr = m.get(p.session) || [];
    arr.push({ ordinal: r.ordinal, id: r.id });
    m.set(p.session, arr);
  }
  return m;
}

// paneCount totals the panes in a layout (1 means a single, unsplit pane — no
// ordinals/map are shown in that case, since there's nothing to disambiguate).
export function paneCount(layout) {
  let n = 0;
  for (const c of layout?.cols || []) n += c.panes.length;
  return n;
}
