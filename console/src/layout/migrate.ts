// Layout (de)serialization + migration from the old console's flat pane format.
//
// The old console persists a wide flat Pane (kind + 13 nullable payload fields)
// under localStorage "af.layout.<slug>". The next console persists the new shape
// (pane.session + discriminated content) under "af.layout2.<slug>" so the two
// entries never clobber each other while running in parallel; when layout2 is
// missing we MIGRATE the old key so the user's split carries over. All input is
// untrusted JSON — validated field by field; anything unusable degrades to a
// blank terminal pane (never a crash), an unusable layout to null.
import type { Cell, Layout, PaneContent, View } from "./types.ts";
import { blankPane } from "./types.ts";
import { equalRatios, MAX_COLS, MAX_TAB_COLS, MAX_TABS } from "./ops.ts";

/* eslint-disable @typescript-eslint/no-explicit-any */

const str = (v: unknown): string | null => (typeof v === "string" && v ? v : null);

/** Old flat pane → new content union. Unknown/incomplete kinds → blank terminal. */
function contentFromFlat(p: any): PaneContent {
  switch (p?.kind) {
    case "terminal":
      return { kind: "terminal", chat: p.chat === true };
    case "file": {
      const filePath = str(p.filePath);
      const targetLine = typeof p.targetLine === "number" && p.targetLine > 0 ? Math.floor(p.targetLine) : undefined;
      const targetColumn = typeof p.targetColumn === "number" && p.targetColumn > 0 ? Math.floor(p.targetColumn) : undefined;
      const mode = p.mode === "view" || p.mode === "edit" ? p.mode : undefined;
      return filePath
        ? { kind: "file", filePath, ...(targetLine ? { targetLine } : {}), ...(targetColumn ? { targetColumn } : {}), ...(mode ? { mode } : {}) }
        : { kind: "terminal", chat: false };
    }
    case "read": {
      const filePath = str(p.filePath);
      return filePath ? { kind: "read", filePath } : { kind: "terminal", chat: false };
    }
    case "scm": {
      const scmRepo = str(p.scmRepo);
      const scmPath = str(p.scmPath);
      return scmRepo ? { kind: "scm", scmRepo, ...(scmPath ? { scmPath } : {}) } : { kind: "terminal", chat: false };
    }
    case "changes": {
      const scmRepo = str(p.scmRepo);
      return scmRepo ? { kind: "changes", scmRepo } : { kind: "terminal", chat: false };
    }
    case "commit": {
      const scmRepo = str(p.scmRepo);
      const scmPath = str(p.scmPath);
      const commitSha = str(p.commitSha);
      return scmRepo && commitSha
        ? { kind: "commit", scmRepo, ...(scmPath ? { scmPath } : {}), commitSha }
        : { kind: "terminal", chat: false };
    }
    case "wtdiff": {
      const scmRepo = str(p.scmRepo);
      const filePath = str(p.filePath);
      return scmRepo && filePath
        ? { kind: "wtdiff", scmRepo, filePath, diffStaged: p.diffStaged === true }
        : { kind: "terminal", chat: false };
    }
    case "doc": {
      const docTitle = str(p.docTitle);
      const docSession = str(p.docSession);
      return docTitle
        ? {
            kind: "doc",
            docTitle,
            docContent: str(p.docContent) || "",
            ...(docSession ? { docSession } : {}),
          }
        : { kind: "terminal", chat: false };
    }
    case "diff": {
      const docTitle = str(p.docTitle);
      // diffEdits も untrusted JSON — 配列でない/要素が壊れている永続値をそのまま
      // 通すと DiffView（.map / e.old アクセス）が throw する。{old,new} の文字列
      // だけを残す形へ正規化する。
      const diffEdits = Array.isArray(p.diffEdits)
        ? p.diffEdits.map((e: any) => ({
            ...(typeof e?.old === "string" ? { old: e.old } : {}),
            ...(typeof e?.new === "string" ? { new: e.new } : {}),
          }))
        : [];
      return docTitle
        ? { kind: "diff", docTitle, diffTool: str(p.diffTool) || "", diffEdits }
        : { kind: "terminal", chat: false };
    }
    case "chat": {
      const conversationId = str(p.conversationId);
      const draftAssistantId = str(p.draftAssistantId);
      return conversationId || draftAssistantId
        ? { kind: "chat", conversationId, draftAssistantId }
        : { kind: "terminal", chat: false };
    }
    case "browser": {
      const port = p.port;
      const path = p.path;
      const validPort = typeof port === "number" && Number.isInteger(port) && port >= 1 && port <= 65535 && port !== 7700;
      const validPath = typeof path === "string" && path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/\\") && !/[\u0000-\u001f\u007f]/.test(path);
      return validPort && validPath
        ? { kind: "browser", port, path }
        : { kind: "terminal", chat: false };
    }
    case "browserAttach": {
      // Opaque ids are deliberately the attachment's only persistent field.
      // Keep the alphabet narrow so a corrupted layout can never become an API
      // path injection when the pane resolves its status after reload.
      const attachmentId = str(p.attachmentId);
      return attachmentId && /^[A-Za-z0-9_-]{1,256}$/.test(attachmentId)
        ? { kind: "browserAttach", attachmentId }
        : { kind: "terminal", chat: false };
    }
    default:
      return { kind: "terminal", chat: false };
  }
}

/** New-format content — re-validated on load (still untrusted JSON). Exported
 * as the shared validator for other untrusted PaneContent inputs (the pop-out
 * handoff payload, layout/popout.ts). */
export function validateStoredContent(c: unknown): PaneContent {
  const o = c as any;
  return contentFromFlat(o ? { ...o, chat: o.chat } : null);
}
const contentFromStored = validateStoredContent;

function viewFrom(raw: any): View | null {
  const id = str(raw?.id);
  if (!id) return null;
  const isNew = raw.content && typeof raw.content === "object";
  const content = isNew ? contentFromStored(raw.content) : contentFromFlat(raw);
  return {
    id,
    // Both formats keep the pane's bound session OUTSIDE the view identity: the
    // old wide struct carried `session` at the top level too (even for file/scm
    // panes — the hidden terminal stayed warm), so this reads the same field.
    session: str(raw.session),
    content,
    wrap: typeof raw.wrap === "boolean" ? raw.wrap : null,
    ...(typeof raw.lastUsedAt === "number" && raw.lastUsedAt > 0 ? { lastUsedAt: raw.lastUsedAt } : {}),
  };
}

function legacyCellFrom(raw: any, cellId: string): Cell | null {
  const selected = viewFrom(raw);
  if (!selected) return null;
  const views: View[] = [selected];
  const seen = new Set([selected.id]);
  for (const tab of Array.isArray(raw.tabs) ? raw.tabs : []) {
    const view = viewFrom(tab);
    if (view && !seen.has(view.id)) { seen.add(view.id); views.push(view); }
  }
  const byId = new Map(views.map((view) => [view.id, view] as const));
  const ordered: View[] = [];
  for (const id of Array.isArray(raw.tabOrder) ? raw.tabOrder : []) {
    const view = typeof id === "string" ? byId.get(id) : undefined;
    if (view) { ordered.push(view); byId.delete(id); }
  }
  for (const view of views) if (byId.has(view.id)) ordered.push(view);
  const syntheticBlank = selected.content.kind === "terminal" && !selected.session && ordered.length === 1;
  return syntheticBlank
    ? { id: cellId, selectedViewId: null, views: [] }
    : { id: cellId, selectedViewId: selected.id, views: ordered };
}

/** normalizeStored turns parsed JSON (old flat OR new format) into a valid
 * Layout, or null when unusable (caller falls back to a fresh layout). */
export function normalizeStored(raw: unknown): Layout | null {
  const l = raw as any;
  const mode = l?.mode === "tabs" ? "tabs" : "split";
  if (!l || !Array.isArray(l.cols) || l.cols.length === 0 || l.cols.length > (mode === "tabs" ? MAX_TAB_COLS : MAX_COLS)) return null;
  const cols = [];
  const seen = new Set<string>();
  for (let ci = 0; ci < l.cols.length; ci++) {
    const c = l.cols[ci];
    const id = str(c?.id);
    const rawCells = Array.isArray(c.cells) ? c.cells : c.panes;
    if (!id || seen.has(id) || !Array.isArray(rawCells)) return null;
    seen.add(id);
    const cells: Cell[] = [];
    for (let ri = 0; ri < rawCells.slice(0, 2).length; ri++) {
      const rc = rawCells[ri];
      let cell: Cell | null;
      if (Array.isArray(rc?.views)) {
        const cellId = str(rc.id);
        if (!cellId) return null;
        const views: View[] = rc.views.map(viewFrom).filter((v: View | null): v is View => !!v);
        const selectedId = str(rc.selectedViewId);
        cell = { id: cellId, views, selectedViewId: selectedId && views.some((v) => v.id === selectedId) ? selectedId : views[0]?.id || null };
      } else {
        cell = legacyCellFrom(rc, `g${ci * 2 + ri}`);
      }
      if (!cell || seen.has(cell.id) || cell.views.some((view) => seen.has(view.id))) return null;
      seen.add(cell.id);
      cell.views.forEach((view) => seen.add(view.id));
      if (mode === "split" && cell.views.length > 1) cell.views = cell.views.slice(0, 1);
      if (!cell.views.some((v) => v.id === cell!.selectedViewId)) cell.selectedViewId = cell.views[0]?.id || null;
      cells.push(cell);
    }
    if (cells.length === 0) continue;
    const rowRatio = typeof c.rowRatio === "number" && c.rowRatio > 0 && c.rowRatio < 1 ? c.rowRatio : 0.5;
    cols.push({ id, rowRatio, cells });
  }
  if (cols.length === 0) return null;
  let colRatios: number[] = Array.isArray(l.colRatios) ? l.colRatios : [];
  if (
    colRatios.length !== cols.length ||
    colRatios.some((r) => typeof r !== "number" || !(r > 0))
  ) {
    colRatios = equalRatios(cols.length);
  } else {
    // 旧クランプの取りこぼし等で合計が 1 からずれた保存値も、復元時に合計 1 へ
    // 正規化する（全要素 > 0 は上の検査で保証済み）。
    const sum = colRatios.reduce((n, r) => n + r, 0);
    colRatios = colRatios.map((r) => r / sum);
  }
  const requestedActive = str(l.activeCellId) || str(l.activeId);
  let activeCellId = cols[0].cells[0].id;
  for (const col of cols) for (const cell of col.cells) {
    if (cell.id === requestedActive || cell.views.some((v) => v.id === requestedActive)) activeCellId = cell.id;
  }
  if (mode === "tabs") {
    const count = cols.reduce((n, c) => n + c.cells.reduce((m, cell) => m + cell.views.length, 0), 0);
    if (count > MAX_TABS) return null;
  }
  return { version: 3, mode, cols, colRatios, activeCellId };
}

/** Storage key: the pane layout is persisted per (user, tenant) so a different
 * account logging in from the same browser can't restore the prior user's session
 * panes (the stale right-pane-session bug). Empty `user` (dev/no-auth) degrades to
 * a per-tenant key. No migration from the old unscoped `af.layout2.<slug>` /
 * `af.layout.<slug>` keys: switching to the user-scoped scheme intentionally
 * resets the layout once rather than attributing a shared layout to whoever logs
 * in first. */
export const LKEY_NEW = (user: string, slug: string, mode: "split" | "tabs" = "split"): string =>
  "af.layout2." + (user || "") + "." + (slug || "") + (mode === "tabs" ? ".tabs" : "");

/** loadStoredLayout reads the persisted layout for a (user, tenant). Returns null
 * when nothing usable is stored.
 *
 * Per-tab first: the tab's own arrangement lives in sessionStorage, which survives
 * a reload but is unique per browser tab and auto-dropped when the tab closes — so
 * several tabs of the same account keep independent pane layouts (open one session
 * per tab and work in parallel). Only when this tab has no copy of its own do we
 * fall back to the shared localStorage entry, which every tab also updates: that
 * seeds a brand-new tab (and an upgrading single-tab user) from the most recently
 * active layout instead of a blank one. Both stores use the same (user, tenant) key. */
export function loadStoredLayout(user: string, slug: string, mode: "split" | "tabs" = "split"): Layout | null {
  const key = LKEY_NEW(user, slug, mode);
  for (const store of [safeSession(), localStorageOr(null)]) {
    if (!store) continue;
    try {
      const s = store.getItem(key);
      if (s) {
        const l = normalizeStored(JSON.parse(s));
        if (l && (l.mode || "split") === mode) return l;
      }
    } catch {
      /* corrupted entry — fall through to the next store / a fresh layout */
    }
  }
  return null;
}

/** sessionStorage guarded for environments (SSR/tests/locked-down browsers) where
 * accessing it throws. Returns null when unavailable. */
function safeSession(): Storage | null {
  try {
    return typeof sessionStorage !== "undefined" ? sessionStorage : null;
  } catch {
    return null;
  }
}
function localStorageOr(fallback: Storage | null): Storage | null {
  try {
    return typeof localStorage !== "undefined" ? localStorage : fallback;
  } catch {
    return fallback;
  }
}

// blankPane is re-exported for tests that build expectations around fallbacks.
export { blankPane };
