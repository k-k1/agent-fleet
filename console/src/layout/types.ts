// Pane-layout domain types for the next console (docs/22). The main area is a
// layout of up to 4 columns; each column stacks 1 or 2 panes — up to 8 panes.
//
// Two deliberate axes (do not conflate):
//   - PaneContent.kind = which VIEW the pane renders (terminal/file/scm/…)
//   - a session's kind = which agent runs in it (claude/codex/…, types/session)
//
// ── The paneId contract (hard invariant, enforced by ops + tests) ──
// A pane's id IS its runtime-view identity: the xterm instance/WebSocket and the
// ephemeral browser Page/WebSocket live in module maps keyed by paneId, and the
// flat-absolute PaneHost keys DOM nodes by it. Operations that move a pane
// (swap, drop-split) MUST relocate the same pane object keeping its id — never
// mint a new id and copy content (a new id builds a fresh xterm + WebGL context
// and blanks the moved terminal). Ids are only allocated for genuinely NEW panes.
//
// ── session lives on the Pane, not in the content union ──
// A pane owns one persistent (possibly hidden) terminal. Showing a file/SCM view
// in the pane hides the terminal but keeps its PTY socket + scrollback warm;
// switching back to the terminal view reveals the same session. So `session` is
// pane-level state tied to the xterm identity, while `content` is just what the
// pane currently renders.

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
  | { kind: "doc"; docTitle: string; docContent: string }
  | { kind: "diff"; docTitle: string; diffTool: string; diffEdits: unknown }
  | { kind: "chat"; conversationId: string | null; draftAssistantId: string | null }
  /** browserId is deliberately absent: Agent Page ids are runtime-only. */
  | { kind: "browser"; port: number; path: string };

export type PaneKind = PaneContent["kind"];

export interface Pane {
  id: string;
  /** Session bound to this pane's persistent xterm (kept warm across view switches). */
  session: string | null;
  content: PaneContent;
  /** Per-pane soft-wrap override for text views (null = follow the global setting). */
  wrap: boolean | null;
}

/** A column: 1 or 2 stacked panes plus the split ratio between them. */
export interface Column {
  id: string;
  rowRatio: number; // top pane's height fraction when the column is split
  panes: Pane[]; // [pane] or [pane, pane]
}

/** The full layout: columns + their width fractions + the active pane id. */
export interface Layout {
  cols: Column[]; // 1–4 columns
  colRatios: number[]; // column width fractions, sums to 1, len == cols.length
  activeId: string; // id of the active pane (click / key target)
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
