// Layout v3 separates geometry from runtime content. A Cell is the stable DOM,
// badge and drop-target identity; a View is a tab and the terminal/browser/editor
// runtime identity. Selecting a tab must never change the containing Cell id.
//
// Two deliberate axes (do not conflate):
//   - PaneContent.kind = which VIEW the pane renders (terminal/file/scm/…)
//   - a session's kind = which agent runs in it (claude/codex/…, types/session)
//
// ── ID contracts (hard invariants, enforced by ops + tests) ──
// Cell.id: geometry, React key, activation, ordinal badge and DnD target.
// View.id: tab, xterm/WebSocket, browser controller and dirty-editor identity.
// Moving or selecting a View preserves both identities. Empty Cells contain no
// synthetic terminal View and therefore allocate no runtime resource.
//
// `session` lives on View, outside PaneContent: it is part of the persistent
// terminal runtime identity and remains bound while that View moves between Cells.

/** What a pane renders. terminal renders the pane's own `session` (chat = the
 * read-mostly Markdown mirror instead of the raw PTY). */
export type PaneContent =
  | { kind: "terminal"; chat: boolean }
  /** `mode` is the opener's request for the starting display mode ("編集で開く"
   * from a file menu); absent = the view picks its own (docs/44 §1.8). */
  | { kind: "file"; filePath: string; targetLine?: number; targetColumn?: number; mode?: "view" | "edit" }
  | { kind: "read"; filePath: string } // 朗読ビュー（docs/24）: 本文を順次読み上げ＋縦書き閲覧
  | { kind: "scm"; scmRepo: string; scmPath?: string }
  | { kind: "changes"; scmRepo: string }
  | { kind: "commit"; scmRepo: string; scmPath?: string; commitSha: string }
  | { kind: "wtdiff"; scmRepo: string; filePath: string; diffStaged: boolean }
  /** In-memory Markdown (a plan). `docSession` records WHICH session the document
   *  was opened from — the plan review surface hangs off it (selection → comment),
   *  and it is deliberately part of the CONTENT, not `pane.session`: that field means
   *  "the session bound to this pane's xterm" and drives terminal attach / rail
   *  badges, which a document must not join. */
  | { kind: "doc"; docTitle: string; docContent: string; docSession?: string }
  | { kind: "diff"; docTitle: string; diffTool: string; diffEdits: unknown }
  | { kind: "chat"; conversationId: string | null; draftAssistantId: string | null }
  /** browserId is deliberately absent: Agent Page ids are runtime-only. */
  | { kind: "browser"; port: number; path: string }
  /**
   * External-owner Chromium view. The short-lived attachment id is the only
   * value persisted in layout state; CDP port/target/URL/credentials stay on
   * the Agent side and are resolved again through the authenticated API.
   */
  | { kind: "browserAttach"; attachmentId: string };

export type PaneKind = PaneContent["kind"];

/** A view is the unit that owns a runtime identity. In the classic layout it
 * is also the visual pane; in the tabbed layout several views live in one
 * visual pane. Keep this identity separate from geometry so moving a tab never
 * recreates its xterm or browser controller. */
export type ViewId = string;
export type CellId = string;
export type ColumnId = string;

export interface View {
  id: ViewId;
  session: string | null;
  content: PaneContent;
  wrap: boolean | null;
  /** Monotonic-ish wall clock used only for the tab-layout LRU replacement. */
  lastUsedAt?: number;
}

/** Compatibility name used by content-oriented consumers. A Pane is now a
 * runtime View, never a geometry cell. */
export type Pane = View;
export type PaneView = View;

export interface Cell {
  id: CellId;
  selectedViewId: ViewId | null;
  /** Array order is the tab order. Empty means a real empty cell: no synthetic
   * disconnected-terminal runtime is allocated merely to represent geometry. */
  views: View[];
}

/** A column: 1 or 2 stacked panes plus the split ratio between them. */
export interface Column {
  id: ColumnId;
  rowRatio: number; // top pane's height fraction when the column is split
  cells: Cell[]; // [cell] or [cell, cell]
}

/** The full layout: columns + their width fractions + the active pane id. */
export interface Layout {
  version: 3;
  /** `split` is the established 4 x 2 one-view-per-pane layout. `tabs` keeps
   * the same grid mechanics but caps it at 3 x 2 and lets each cell own tabs. */
  mode?: "split" | "tabs";
  cols: Column[]; // 1–4 columns
  colRatios: number[]; // column width fractions, sums to 1, len == cols.length
  activeCellId: CellId;
}

/** What an open-operation targets: the view to show, plus (for terminals) the
 * session to bind. session undefined = keep the pane's current session (a bare
 * "switch back to the terminal view"); a string = bind that session. */
export interface OpenTarget {
  content: PaneContent;
  session?: string | null;
}

export const BLANK_TERMINAL: PaneContent = { kind: "terminal", chat: false };

export const blankPane = (id: string): Pane => ({
  id,
  session: null,
  content: { ...BLANK_TERMINAL },
  wrap: null,
});

export const paneView = (pane: Pane): PaneView => ({ ...pane });

export const emptyCell = (id: CellId): Cell => ({ id, selectedViewId: null, views: [] });
