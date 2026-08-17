// transcript/types — the wire + view shapes of one conversation transcript.
//
// These are shared by every consumer of the transcript rendering layer: the mirror
// (features/mirror/MirrorView.tsx, the session's owner) and the shared-session view
// (features/sharing/SharedSessionView.tsx, a recipient reading someone else's session
// through the control-plane's allowlist DTO). Keep them free of owner-only concepts —
// anything that only makes sense while you can *drive* the session belongs in the
// caller, not here.

export interface QuestionOption {
  label: string;
  description?: string;
  // A mockup / snippet attached to this option so the choices can be compared before one
  // is picked (claude's AskUserQuestion `preview`). Rendered verbatim — see optionPreview.
  preview?: string;
}

export interface Question {
  id?: string; // managed: 応答先 Interaction の id（docs/27 §5）。tui 由来は空
  header?: string;
  question?: string;
  multiSelect?: boolean;
  options?: QuestionOption[];
}

// One ToDo task, reconstructed server-side from the transcript's Task tool calls.
export interface TaskItem {
  id: string;
  subject: string;
  activeForm?: string;
  status: string; // "pending" | "in_progress" | "completed"
}

// One ordered part of a turn (Markdown text, a tool trace, a question, or a plan).
export interface Part {
  kind: string;
  text?: string;
  tool?: string;
  info?: string;
  // kind=error: なぜ失敗したかの機械可読な軸（"auth" = 再認証で直る、空 = 導線なし）。
  // info はエージェント自身のエラー名で版ごとに変わるため、文言一致で導線を出しては
  // いけない — 分類はエージェント側（各 errors.go）が持ち、ここはその印だけを見る。
  cause?: string;
  output?: string;
  prompt?: string; // kind=delegation: full instruction sent to the child
  agentType?: string; // kind=delegation: Explore/general-purpose/task name
  status?: string; // kind=delegation: requested/running/completed/failed
  model?: string; // kind=delegation: explicitly selected child model
  file?: string;
  edits?: any[];
  // verb qualifies `file` for the changed-files list (docs/68): "add" | "edit" | "delete".
  // Only a parser that KNOWS sends it (codex reads it out of the patch header); absent
  // means "derive it from the edits", never "deleted".
  verb?: string;
  questions?: Question[];
  answer?: string;
  // declined marks kind=question only: the answer text is claude's own decline
  // boilerplate (an Escape out of the AskUserQuestion modal — e.g. the preview
  // free-text bug, docs/dev/92 §6), not a genuine pick — QuestionBlock must not render
  // it as an answered card.
  declined?: boolean;
  plan?: string;
  qid?: string; // kind=question/plan/delegation: tool_use id, to patch a late answer (see patchAnswers)
  files?: string[]; // kind=userfile: SendUserFile paths (browse-root-relative)
  caption?: string; // kind=userfile: optional caption
  stderr?: string; // kind=bash: the ! command's stderr (output field holds stdout)
}

export interface Turn {
  role: string;
  text?: string;
  ts?: string;
  // endTs: when an assistant turn finished, sent only by agents that record a whole turn
  // as ONE row (opencode, copilot) — for them ts alone is the turn's START. Agents that
  // write a turn as many rows (claude, codex) omit it; groupTurns derives the end from the
  // last row it folds in. See workspace/agent/internal/transcript/transcript.go.
  endTs?: string;
  idx?: number;
  // anchorId: the AGENT's own id for this turn (claude uuid / codex turn id / opencode
  // msg_…), opaque here — it is handed straight back to POST /fork {"at"} to branch from
  // this point (docs/55). Absent for kinds that don't emit one, and for local echoes.
  anchorId?: string;
  pending?: boolean; // optimistic local echo of a just-sent prompt, not yet in the jsonl
  queued?: boolean; // sitting in claude's mid-run queue (enqueued, awaiting injection)
  source?: string; // user turn origin: "operator" = fleet-operator injected (docs/30 ②), else own input
  parts?: Part[];
  sidechain?: boolean;
  compact?: boolean;
  bash?: boolean; // a `!`-run shell command block (coalesceUserActions), rendered as a terminal block
  cmd?: boolean; // a `/`-run slash command / skill invocation, rendered as a compact chip
  model?: string;
  effort?: string;
  ctxWindow?: number;
  branch?: string;
  cwd?: string;
  inTok?: number;
  outTok?: number;
  cacheRead?: number;
  cacheCreate?: number;
}

export interface Group {
  role: string;
  sidechain: boolean;
  compact: boolean;
  bash: boolean; // a `!` shell-command block — render as a terminal block, never merged
  cmd: boolean; // a `/` slash-command / skill chip — standalone, never merged
  parts: Part[];
  text: string;
  model: string;
  effort: string;
  ctxWindow: number;
  branch: string;
  cwd: string;
  inTok: number;
  outTok: number;
  cacheRead: number;
  cacheCreate: number;
  ts?: string; // the block's START (first folded turn) — ordering key, see chronoInsertIndex
  endTs?: string; // the block's END (last folded turn) — what the footer shows
  idx?: number;
  // The FIRST folded turn's anchor — branching "from this block" means branching before
  // everything it shows, so a merged block must not adopt a later turn's anchor.
  anchorId?: string;
  pending?: boolean; // holds an optimistic local echo awaiting its real transcript turn
  queued?: boolean; // holds a prompt claude reports queued for the running turn
  source?: string; // user group origin: "operator" = fleet-operator injected (docs/30 ②)
}

// foldParts output: a run of tool traces, or a single passthrough part.
export type FoldItem =
  | { kind: "toolrun"; tools: { p: Part; i: number }[] }
  | { kind: "part"; p: Part; i: number };

// カラオケ朗読（turnTts, docs/24）の配線。いま読み上げ中のターン（transcript の idx）と
// 操作を TranscriptView 経由で各ターンのフッターへ渡す。ハイライトは turnTts が DOM
// （classList）側で行い、React はボタンの表示切り替えだけを持つ。
export interface TurnTtsWiring {
  reading: { idx: number; paused: boolean } | null;
  start: (idx: number, body: HTMLElement) => void;
  pause: () => void;
  resume: () => void;
  stop: () => void;
}
