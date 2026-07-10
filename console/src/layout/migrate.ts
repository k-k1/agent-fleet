// Layout (de)serialization + migration from the old console's flat pane format.
//
// The old console persists a wide flat Pane (kind + 13 nullable payload fields)
// under localStorage "af.layout.<slug>". The next console persists the new shape
// (pane.session + discriminated content) under "af.layout2.<slug>" so the two
// entries never clobber each other while running in parallel; when layout2 is
// missing we MIGRATE the old key so the user's split carries over. All input is
// untrusted JSON — validated field by field; anything unusable degrades to a
// blank terminal pane (never a crash), an unusable layout to null.
import type { Layout, Pane, PaneContent } from "./types.ts";
import { blankPane } from "./types.ts";
import { equalRatios, MAX_COLS } from "./ops.ts";

/* eslint-disable @typescript-eslint/no-explicit-any */

const str = (v: unknown): string | null => (typeof v === "string" && v ? v : null);

/** Old flat pane → new content union. Unknown/incomplete kinds → blank terminal. */
function contentFromFlat(p: any): PaneContent {
  switch (p?.kind) {
    case "terminal":
      return { kind: "terminal", chat: p.chat === true };
    case "file": {
      const filePath = str(p.filePath);
      return filePath ? { kind: "file", filePath } : { kind: "terminal", chat: false };
    }
    case "read": {
      const filePath = str(p.filePath);
      return filePath ? { kind: "read", filePath } : { kind: "terminal", chat: false };
    }
    case "scm": {
      const scmRepo = str(p.scmRepo);
      return scmRepo ? { kind: "scm", scmRepo } : { kind: "terminal", chat: false };
    }
    case "changes": {
      const scmRepo = str(p.scmRepo);
      return scmRepo ? { kind: "changes", scmRepo } : { kind: "terminal", chat: false };
    }
    case "commit": {
      const scmRepo = str(p.scmRepo);
      const commitSha = str(p.commitSha);
      return scmRepo && commitSha
        ? { kind: "commit", scmRepo, commitSha }
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
      return docTitle
        ? { kind: "doc", docTitle, docContent: str(p.docContent) || "" }
        : { kind: "terminal", chat: false };
    }
    case "diff": {
      const docTitle = str(p.docTitle);
      return docTitle
        ? { kind: "diff", docTitle, diffTool: str(p.diffTool) || "", diffEdits: p.diffEdits }
        : { kind: "terminal", chat: false };
    }
    case "chat": {
      const conversationId = str(p.conversationId);
      const draftAssistantId = str(p.draftAssistantId);
      return conversationId || draftAssistantId
        ? { kind: "chat", conversationId, draftAssistantId }
        : { kind: "terminal", chat: false };
    }
    default:
      return { kind: "terminal", chat: false };
  }
}

/** New-format content — re-validated on load (still untrusted JSON). */
function contentFromStored(c: any): PaneContent {
  return contentFromFlat(c ? { ...c, chat: c.chat } : null);
}

function paneFrom(raw: any): Pane | null {
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
  };
}

/** normalizeStored turns parsed JSON (old flat OR new format) into a valid
 * Layout, or null when unusable (caller falls back to a fresh layout). */
export function normalizeStored(raw: unknown): Layout | null {
  const l = raw as any;
  if (!l || !Array.isArray(l.cols) || l.cols.length === 0 || l.cols.length > MAX_COLS) return null;
  const cols = [];
  const seen = new Set<string>();
  for (const c of l.cols) {
    const id = str(c?.id);
    if (!id || seen.has(id) || !Array.isArray(c.panes)) return null;
    seen.add(id);
    const panes: Pane[] = [];
    for (const rp of c.panes.slice(0, 2)) {
      const p = paneFrom(rp);
      if (!p || seen.has(p.id)) return null;
      seen.add(p.id);
      panes.push(p);
    }
    if (panes.length === 0) continue;
    const rowRatio = typeof c.rowRatio === "number" && c.rowRatio > 0 && c.rowRatio < 1 ? c.rowRatio : 0.5;
    cols.push({ id, rowRatio, panes });
  }
  if (cols.length === 0) return null;
  let colRatios: number[] = Array.isArray(l.colRatios) ? l.colRatios : [];
  if (
    colRatios.length !== cols.length ||
    colRatios.some((r) => typeof r !== "number" || !(r > 0))
  ) {
    colRatios = equalRatios(cols.length);
  }
  const activeId =
    cols.some((c) => c.panes.some((p) => p.id === l.activeId)) && typeof l.activeId === "string"
      ? l.activeId
      : cols[0].panes[0].id;
  return { cols, colRatios, activeId };
}

/** Storage keys: new console writes layout2; the old console's layout is the
 * migration source (read-only from here — the frozen console still owns it). */
export const LKEY_NEW = (slug: string): string => "af.layout2." + (slug || "");
export const LKEY_OLD = (slug: string): string => "af.layout." + (slug || "");

/** loadStoredLayout reads (and if needed migrates) the persisted layout for a
 * tenant. Returns null when nothing usable is stored. */
export function loadStoredLayout(slug: string): Layout | null {
  for (const key of [LKEY_NEW(slug), LKEY_OLD(slug)]) {
    try {
      const s = localStorage.getItem(key);
      if (!s) continue;
      const l = normalizeStored(JSON.parse(s));
      if (l) return l;
    } catch {
      /* corrupted entry — try the next source */
    }
  }
  return null;
}

// blankPane is re-exported for tests that build expectations around fallbacks.
export { blankPane };
