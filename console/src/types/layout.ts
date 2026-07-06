// Main-area pane layout types. The main area is a "layout" of up to 4 columns
// shown side by side; each column can split top/bottom into 2 rows — up to 8
// panes. Mirrors the runtime shape built in state.jsx (blankPane / initialLayout).
//
// NOTE: a pane's `kind` (which VIEW renders — terminal/file/scm/doc/diff) is a
// DIFFERENT axis from a *session's* kind (claude/codex/opencode/shell/ssm). Keep
// them distinct: PaneKind here, SessionKind in types/session.

// Which view a pane renders. The *Path/Repo/doc/diff fields are its per-kind payload.
// scm = the repo's commit-graph; changes = its working-tree changes + commit box;
// commit = one commit's detail/diff (scmRepo + commitSha).
// wtdiff = a single working-tree file's diff (scmRepo + filePath + diffStaged), opened
// in its own pane from the 変更 view (like commit opens a commit's diff).
export type PaneKind = "terminal" | "file" | "scm" | "changes" | "commit" | "doc" | "diff" | "wtdiff" | "chat";

// A single pane descriptor. An empty terminal pane (session null) shows
// "セッション未接続".
export interface Pane {
  id: string;
  kind: PaneKind;
  session: string | null; // session name attached to a terminal pane
  chat: boolean; // terminal pane showing the claude chat mirror instead of the PTY
  filePath: string | null; // file view target (home-relative)
  scmRepo: string | null; // source-control view target repo (scm / changes / commit / wtdiff)
  commitSha: string | null; // commit-detail view target sha (commit kind)
  diffStaged: boolean | null; // wtdiff: whether to show the staged (index) diff vs the worktree
  docTitle: string | null; // ad-hoc doc view title
  docContent: string | null; // ad-hoc doc view markdown/text
  diffTool: string | null; // diff view: originating tool label
  diffEdits: unknown; // diff view: edit payload (shape owned by DiffView)
  conversationId: string | null; // chat view: assistant-chat conversation id (docs/19)
  draftAssistantId: string | null; // chat view: a not-yet-created draft for this assistant (docs/19)
  wrap: boolean | null; // per-pane soft-wrap override (null = follow global setting)
}

// A column: 1 or 2 stacked panes plus the split ratio between them.
export interface Column {
  id: string;
  rowRatio: number; // top pane's height fraction when the column is split
  panes: Pane[]; // [pane] or [pane, pane]
}

// The full layout: columns + their width fractions + the active pane id.
export interface Layout {
  cols: Column[]; // 1–4 columns
  colRatios: number[]; // column width fractions, sums to 1, len == cols.length
  activeId: string; // id of the active pane (click / key target)
}
