import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, KeyboardEvent as RKeyboardEvent, ClipboardEvent as RClipboardEvent, DragEvent as RDragEvent, ReactNode } from "react";
import { api, apiJSON, raw, errText, pasteImage, sessionTurn, sessionRespond, sessionPlanRespond, sessionSettings, downloadURL } from "../../core/api/client.ts";
import type { CarriedInteraction, InteractionAnswer, ManagedThreadSettings, TurnResult } from "../../core/api/client.ts";
import { isManagedSession } from "../../types/session.ts";
import type { Session } from "../../types/session.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { MEMO_DND_MIME } from "../memo/dnd.ts";
import {
  useSettings,
  setSetting,
  chatFontStack,
  surfaceBg,
  surfaceAccent,
  effectiveTheme,
  expandThinking,
} from "../../lib/settings.ts";
import { isQuickReplyCandidate, isQuickReplyPinned, recordQuickReply, unhideQuickReply } from "../../lib/quickReplies.ts";
import { SuggestChipMenu } from "./SuggestChipMenu.tsx";
import { useLayoutStore } from "../../layout/store.ts";
// 失敗ブロックの再認証導線が 設定 > エージェント を開くのに使う（ErrorBlock）。
import { useSettingsUI } from "../settings/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useDraft, writeDraft } from "../../lib/draft.ts";
import { autoGrowTextarea } from "../../lib/autoGrow.ts";
import { scrollComposerViewport } from "../../lib/keyScroll.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { prettyModel } from "../../lib/modelName.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { MirrorToggle } from "./MirrorToggle.tsx";
import { MirrorBanners } from "./parts/MirrorBanners.tsx";
import { useMirrorTts } from "./parts/useMirrorTts.tsx";
import { useSkillPicker } from "./parts/useSkillPicker.ts";
import { useReplySuggest } from "./parts/useReplySuggest.ts";
import { JumpPills } from "./parts/JumpPills.tsx";
import { AttachChips } from "./parts/AttachChips.tsx";
import { HistoryNav } from "./parts/HistoryNav.tsx";
import { SendColumn } from "./parts/SendColumn.tsx";
import { SkillButton, SkillList } from "./parts/SkillList.tsx";
import { SuggestRow } from "./parts/SuggestRow.tsx";
import {
  DirGoneNotice,
  ResumeNotice,
  ResumingNotice,
  TerminalResumeNotice,
  TerminalUpdateNotice,
  WsStoppedNotice,
} from "./parts/ComposerNotices.tsx";
import { ContextBar } from "./ContextBar.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t as tr, useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { takeLaunchSeed } from "../../lib/launchSeed.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { PaneSessionChip } from "../panes/PaneSessionChip.tsx";
// workSplit はターン描画とともに transcript/ へ移った（TranscriptTurn が持つ）。
import { awaitingReply, latestWorkPromptIndex, textOfParts } from "./mirrorParts.ts";
import { echoLanded, echoNeedsResync } from "./pendingEcho.ts";
import { echoStore, nextEchoId, type SendEcho } from "./parts/sendEcho.ts";
import { findDiffPane, findPane, findPlanPane } from "./parts/panes.ts";
import { applyMark, captureMark, saveMark, scrollTopForTurn, loadMark, type ScrollMark } from "./scrollMark.ts";
import { PLAN_APPROVE_KEYS } from "./planDecision.ts";
import { deliverPlanComments, planKey } from "./planComments.ts";
import { type InteractionAnswerWire, patchAnswers } from "./interactionAnswers.ts";
import { coarsePointer } from "../../lib/device.ts";
import { ManagedSettingsModal } from "./ManagedSettingsModal.tsx";
import { ForkAtModal } from "./ForkAtModal.tsx";
import type { ForkAtTarget } from "./ForkAtModal.tsx";
import { canBranchFrom, canBranchInSession, carriedUserTurns } from "./forkAt.ts";
import { HandoffProposal, useHandoffProposals, type Proposal as HandoffProposalT } from "./HandoffProposal.tsx";
import { PlanPendingCard, PermissionCard, QuestionCard, TypingRow } from "./parts/pendingCards.tsx";
import { CarriedBlock } from "./CarriedBlock.tsx";
import { FileChangeStrip } from "./FileChangeStrip.tsx";
import { useSessionFilesStore, type SessionFile } from "./sessionFiles.ts";
// The transcript rendering layer, shared with the shared-session view (docs/log/59). What the
// reader may DO here is expressed as TranscriptCaps — the mirror is the owner, so it fills
// in every capability; a recipient fills in almost none. See transcript/capabilities.ts.
import { TranscriptView } from "./transcript/TranscriptView.tsx";
import type { TranscriptCaps } from "./transcript/capabilities.ts";
import type { Group, Part, Question, TaskItem, Turn } from "./transcript/types.ts";
import { coalesceUserActions, groupTurns, isNoise, latestContext, parseCommand, spendOf } from "./transcript/model.ts";
import { TaskChecklist, planTitle } from "./transcript/blocks.tsx";
import { useMarksController } from "./transcript/useMarks.ts";
import { MarkStrip } from "./transcript/MarkStrip.tsx";

const q = encodeURIComponent;

// Transcript window size (jsonl lines) for the initial tail load and each backward page.
// The server clamps it; matches docs/decisions/0009 (P2).
const WINDOW = 400;

// How long the "working" indicator is held after a turn reads idle while its reply is
// still not in the transcript (the idle→reply-renders gap). Long enough to cover the
// jsonl-write / poll-cadence lag, short enough that a genuinely reply-less turn (e.g. an
// interrupt) doesn't leave a phantom spinner. See `finalizing`.
const FINALIZE_GRACE_MS = 8000;

// The user counts as "stuck to the bottom" (auto-follow on) while within this many px of
// the end. Above it, following stops and the jump-to-latest button appears. Narrower than
// before by request, so follow drops more readily on scroll-up — note this sits close to
// the typing indicator / stop-button row's ~40–60px height swing between polls, so that
// swing can occasionally nudge us out of "at bottom" on its own.
const NEAR_BOTTOM_PX = 80;

// After an interaction inside the transcript, hold off the bottom re-pin for this long, so
// content the READER grew (expanding a 作業過程 disclosure, switching code wrapping) keeps
// their position instead of snapping past it. Only needs to outlive the reflow the click
// causes — everything else that grows the transcript is content, and is followed.
const INTERACT_HOLD_MS = 600;

// 「返信を頭から」の頭出しで、返信ブロックの上端に残す余白（px）。0 だと切り出しに見える。
const REPLY_TOP_PAD = 8;

// MirrorView (user-facing: チャット) is a read-mostly Markdown view of a claude
// session, built on the same Agent endpoints the MCP drive tools use: GET
// /sessions/{name}/messages?since=<cursor> (the jsonl transcript as structured turns
// — role + Markdown text + timestamp — plus a line cursor and live status) and POST
// /sessions/{name}/input (tmux send-keys). It overlays the still-mounted terminal
// (Pane keeps the PTY socket alive), so the user toggles ターミナル⇄チャット freely.
//
// Limits (case-A): the transcript is written per turn, so turns appear per response,
// not token-by-token. Prompts typed in the raw terminal DO appear (they're logged as
// user turns), just at the next poll.
export function MirrorView({
  paneId,
  session,
  sessionMeta,
  active,
  mirror,
  onToggleMirror,
  readOnly = false,
  onResume,
  headerActions,
}: {
  paneId: string;
  session: string;
  sessionMeta?: Session | null;
  active?: boolean;
  mirror?: boolean;
  onToggleMirror: (v: boolean) => void;
  readOnly?: boolean;
  onResume?: () => void;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}) {
  const settings = useSettings();
  // Per-agent descriptor: how this session's assistant signs its turns, and which
  // chat affordances (image paste, …) it supports. Defaults to claude for a not-yet
  // loaded meta. codex/opencode reuse the same chat; only these bits differ.
  const agent = agentOf(sessionMeta?.kind);
  // Managed（paneless）セッション: ターミナルが存在しないので、ミラーが主 UI。
  // トグルを出さず、質問応答は keys/seq でなく Interaction 応答（/respond）で送る。
  const managed = isManagedSession(sessionMeta);
  const agentName = agent.assistantName;
  const canPasteImage = agent.caps.imagePaste;
  // Store bridge (old context values): plans open as doc panes, edit-diffs as
  // diff panes; bumpSessions refreshes the shared list; wsState gates attach.
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const setActivePane = useLayoutStore((s) => s.setActive);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const bumpSessions = () => void refreshSessions();
  const wsState = useWorkspaceStore((s) => s.state);
  const toast = useToast();
  useT(); // subscribe: a locale change re-renders MirrorView and its (unmemoized) turn subtree
  const running = wsState === "running"; // WS down → resume is inert, mirror the terminal 再開
  // "mod-enter" (default): Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe).
  // "enter": Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [turns, setTurns] = useState<Turn[]>([]); // {role:'user'|'assistant', text, ts, idx}
  // Which session the accumulated view state (turns / alive / mode / echoes …) belongs to.
  // A pane keeps this component mounted while its `session` prop changes (PaneHost keys a
  // cell, not a session), and the per-session reset is a layout effect — so on that one
  // commit every piece of state below is still the PREVIOUS session's. Anything that reads
  // state to decide something about the NEW session must wait for `stateSession === session`.
  const [stateSession, setStateSession] = useState(session);
  // Optimistic local echoes of just-sent prompts. While claude is working it queues a new
  // prompt WITHOUT logging it to the jsonl until the current turn finishes, so the mirror
  // (transcript-only) would show nothing — the message looks lost. We render these until
  // the matching real user turn appears, then reconcile them away. sinceIdx = the newest
  // real turn idx at send time, so we only match a turn that arrives AFTER the send.
  const [pendingSends, setPendingSends] = useState<SendEcho[]>(() => echoStore.get(session) ?? []);
  // Every echo update goes through here so the module stash stays in sync (write-through)
  // and a remounted view can restore the un-landed ones.
  // …and the poll loop (a [session]-only effect) reads them through a ref, so its
  // stuck-echo self-heal never works off a stale closure.
  const pendingSendsRef = useRef<SendEcho[]>(echoStore.get(session) ?? []);
  const applyEchoes = (fn: (prev: SendEcho[]) => SendEcho[]) =>
    setPendingSends((prev) => {
      const next = fn(prev);
      echoStore.set(session, next);
      pendingSendsRef.current = next;
      return next;
    });
  const [loaded, setLoaded] = useState(false); // false until the first transcript fetch returns
  // This session's outstanding handoff proposals (possibly more than one — a single turn
  // can fan a task out into several parallel follow-ups). Owned here (not inside the
  // card) because each card is placed at its created_at inside the transcript, not
  // pinned to the bottom.
  const [handoffs, setHandoffs] = useHandoffProposals(session);
  const updateHandoff = (id: string, next: HandoffProposalT | null) =>
    setHandoffs(next ? handoffs.map((h) => (h.id === id ? next : h)) : handoffs.filter((h) => h.id !== id));
  const [termState, setTermState] = useState(""); // terminal-only state: "resume" | "compacting" | "update" | ""
  // Compaction progress (parsed from the pane) so the 圧縮中 block shows a bar, not just a spinner.
  const [compactProg, setCompactProg] = useState<{ pct: number; elapsed?: string } | null>(null);
  const [status, setStatus] = useState("");
  const [bgBusy, setBgBusy] = useState(false); // idle but a run_in_background task lingers
  const [bgBusyReason, setBgBusyReason] = useState(""); // WHAT lingers: process | subagent | shell
  // "Finalizing" bridges the gap between claude finishing (status flips to idle — its
  // Stop hook, or the TUI heal firing once the spinner clears during answer streaming)
  // and the reply actually landing in the transcript jsonl a poll later. In that window
  // the naive indicator would blink off over an empty mirror, so the user sees the
  // spinner vanish with no answer yet and thinks it stalled. While finalizing we keep the
  // typing indicator up and keep polling fast until the reply renders (or a grace lapses).
  const [finalizing, setFinalizing] = useState(false);
  const finalizingRef = useRef(false);
  const wasWorkingRef = useRef(false); // saw "working" since the last landed reply
  // The exchange is still in flight. Everything that reacts to "is a turn running" must use
  // THIS, not the bare polled status: the status alone drops to idle mid-answer (Stop hook /
  // TUI heal) and says nothing about a background run. The typing indicator, the bottom
  // follow and the 作業過程 fold all read it, so they can't disagree — a fold that flips
  // while the spinner is still up is exactly what shifts the text under a reader.
  const busy = status === "working" || bgBusy || finalizing;
  // Show a "jump to latest ↓" affordance whenever the user has scrolled up off the bottom
  // (auto-follow is paused) so new/streaming content below is discoverable with one click.
  const [showJump, setShowJump] = useState(false);
  // 「返信を頭から」— 最新の回答ブロックの先頭が画面より上に流れていて、かつ末尾追従が切れて
  // いるときだけ出す（末尾では出さない: 押すべきボタンの上に被るため。syncReplyTop の注記）。
  const [showReplyTop, setShowReplyTop] = useState(false);
  const [tasks, setTasks] = useState<TaskItem[]>([]); // current ToDo list (Task tool calls)
  // Files this session's agent edited (docs/log/68). Aggregated server-side over the WHOLE
  // transcript and delivered on this same poll — deriving it from `turns` would count
  // only the window the mirror happens to hold and grow as the reader scrolls up.
  const [files, setFiles] = useState<SessionFile[]>([]);
  // Prompts claude reports queued into the RUNNING turn (queue-operation events) — sent
  // mid-run from this composer or typed in the raw terminal, not yet injected. Matching
  // echoes get a キュー済み badge; the rest render as synthetic queued bubbles.
  const [queuedPrompts, setQueuedPrompts] = useState<string[]>([]);
  const [alive, setAlive] = useState(!!sessionMeta?.alive); // live session ⇒ composer usable
  // The working dir was removed (repo/worktree deleted): the transcript survives
  // (stored under the agent's home), so history stays readable, but resume is
  // impossible — BuildLaunch refuses a gone dir. Offer a note, not a resume button.
  const dirGone = sessionMeta?.resumable === false && !alive;
  const [pending, setPending] = useState<Question[] | null>(null); // currently-awaiting AskUserQuestion
  const [pendingText, setPendingText] = useState<string>(""); // prose streamed just before the pending question
  const [pendingPlan, setPendingPlan] = useState<string | null>(null); // ExitPlanMode plan awaiting approval
  const [pendingPerm, setPendingPerm] = useState<string | null>(null); // tool-permission prompt awaiting allow/deny
  // 持ち越し（docs/log/75）: セッションが畳まれたときに画面に出ていた対話。保留（上の 3 つ）と
  // 違ってモーダルはもう無いので、答えはキーではなく文章として配達される。サーバは保留が
  // あるあいだ carried を出さないので、両方が同時に立つことはない。
  const [carried, setCarried] = useState<CarriedInteraction | null>(null);
  // Plans the user just 却下'd (keyed by plan text). Lets the historical plan badge show
  // 却下 immediately, before the interrupt tool_result (its real signal) lands a poll or
  // two later — otherwise it sits at the neutral 決定済み until then.
  const rejectedPlansRef = useRef<Set<string>>(new Set());
  const [mode, setMode] = useState(""); // session permission mode ("plan" | …)
  // 直近に端末が名乗った非 plan モード名。plan を抜けたときの楽観ラベルに使う（docs/log/76）。
  const lastNonPlanMode = useRef("");
  // Session-level context fill reported by the agent itself (agy /context scrape) —
  // the ContextBar's fallback when the transcript has no per-turn token usage.
  const [agentCtx, setAgentCtx] = useState<{ tokens: number; window: number } | null>(null);
  const [suggestedTitle, setSuggestedTitle] = useState(""); // headless-LLM title candidate, "" = none
  const [titleActing, setTitleActing] = useState(false); // accept/dismiss request in flight
  const [managedSettingsOpen, setManagedSettingsOpen] = useState(false);
  const [managedSettings, setManagedSettings] = useState<ManagedThreadSettings | null>(null);
  // 「ここから分岐」の確認待ち（docs/log/55）。null = 閉じている。
  const [forkAtTarget, setForkAtTarget] = useState<ForkAtTarget | null>(null);
  // Composer draft, persisted per session so switching ターミナル⇄チャット (which
  // unmounts this view) — or a reload — keeps what you were typing. Key by session.
  const draftKey = session ? "af.mirror-draft." + session : null;
  const [draft, setDraft] = useDraft(draftKey);
  const [sending, setSending] = useState(false);
  // sendingRef mirrors `sending` for a synchronous re-entrancy check. `sending` alone
  // (React state) isn't enough: two send() invocations arriving in the same task (Enter
  // auto-repeat, an IME compositionend immediately followed by its own keydown, or a
  // stray double click before the button's `disabled` re-render commits) both read the
  // stale pre-update value and both pass the `sending` guard in sendPrompt — producing
  // two real POST /turn calls for what was one user action. The duplicate then depends on
  // codex's own handling of an immediate identical resubmission (observed: silently
  // absorbed into nothing), leaving the second optimistic echo with no turn to reconcile
  // against — stuck at 反映待ち forever. Set/read synchronously, before any state commit.
  const sendingRef = useRef(false);
  // Pasted images awaiting send: {path} is the session-saved absolute path (referenced in
  // the prompt), {url} an object URL for the local chip preview, {name} the basename.
  const [attachments, setAttachments] = useState<{ path: string; name: string; url: string; image: boolean }[]>([]);
  const [pasting, setPasting] = useState(false); // an attachment upload is in flight
  const [dragging, setDragging] = useState(false); // an OS file drag is hovering the pane
  const dragDepth = useRef(0); // dragenter/leave nesting counter (leave fires per child)
  const filePickRef = useRef<HTMLInputElement>(null); // the ＋ button's hidden picker
  const [lightbox, setLightbox] = useState<string | null>(null); // enlarged image (blob URL) or null
  // Close the enlarged-image lightbox with the device/browser Back button or a back gesture
  // (phones foremost): opening it pushes a throwaway history entry, so Back pops that instead
  // of navigating away from the Console; a tap on the backdrop consumes the entry on cleanup.
  useBackClose(lightbox ? () => setLightbox(null) : undefined, !!lightbox);
  const [histIdx, setHistIdx] = useState<number | null>(null); // position in composer history, or null
  const cursorRef = useRef(0);
  // Backward paging (P2): firstLineRef = oldest jsonl line currently held; hasMore = there
  // is older history above it to page in. loadingOlderRef guards against overlapping loads;
  // prependAdjustRef carries the pre-prepend scrollHeight so we can pin the viewport.
  const firstLineRef = useRef(0);
  const [hasMore, setHasMore] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const loadingOlderRef = useRef(false);
  const prependAdjustRef = useRef<number | null>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const diagRef = useRef(""); // last transcript-diagnostic signature (warn once per change)
  const statusRef = useRef("");
  const bgBusyRef = useRef(false); // mirrors bgBusy for the poll-cadence closure (fast-poll while BG runs)
  const tickRef = useRef<(() => void) | null>(null); // lets send() trigger an immediate refresh
  const mirrorRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrollBoxRef = useRef<HTMLDivElement>(null); // inner content wrapper — its height tracks the transcript
  const inputRef = useRef<HTMLTextAreaElement>(null);
  // Is auto-follow on (keep the end of the transcript in view)? This tracks the user's
  // INTENT, not raw geometry: it goes false when they actually move the viewport up, and
  // true again when they come back to the end (or send, or press 最新へ). Content growing
  // under a pinned viewport is not a reason to drop it — see onBodyScroll.
  const atBottomRef = useRef(true);
  // The scrollTop WE last wrote. onBodyScroll compares against it to tell "content grew
  // under our own pin" (not user intent) from "the user scrolled up" — see there.
  const selfTopRef = useRef(0);
  // Until this ms, a geometry change is attributed to the reader's own click, not to content.
  const interactUntilRef = useRef(0);
  // 位置復元（scrollMark）: このセッションに戻ってきたときに復元すべき位置。復元中（= restoring
  // が true）は、末尾ピンではなくこのアンカーを保つ。
  //
  // 時間で切らないのは末尾ピンと同じ理由 — 高さは何段にも分かれて遅れて確定し、その最後の一段が
  // いつ来るかは端末しだい。実測（4x スロットリング / 400 ターン）では、遅延レイアウトが 1 回の
  // 大きなコミットで片付き、ResizeObserver が鳴ったのは着地の 3.6 秒後だった。3 秒で切る設計だと
  // その 1 回を取りこぼし、目的地の 24〜729px 手前で固まる。抜けるのは「読者が触った」ときと
  // 「末尾追従に戻った」とき（送信・最新へ）だけにする。
  const restoreMarkRef = useRef<ScrollMark | null>(null);
  const restoringRef = useRef(false);
  // 「返信を頭から」の対象＝最新の回答ブロックの idx。レンダごとに書き、[] で作られる
  // ResizeObserver / onScroll のクロージャからも今の値が読めるようにする（ttsCaptureRef と同型）。
  const lastReplyIdxRef = useRef<number | undefined>(undefined);
  // The idx of the assistant block whose TOP we last brought to the viewport top. A fresh
  // reply is anchored there once (so the user reads it from its first line) and then left
  // alone as it streams; this remembers which reply we've already anchored.
  const anchoredIdxRef = useRef<number | undefined>(undefined);
  // The idx of the reply whose FINAL ANSWER top we've already brought to the viewport top.
  // On completion a following pane collapses the 作業過程 into a disclosure, so the reply's
  // top becomes that collapsed row; we then re-anchor once to the final answer's first line
  // (docs/log/24). Kept separate from anchoredIdxRef so the top-anchor and the answer-anchor each
  // fire exactly once per reply.
  const answerAnchoredRef = useRef<number | undefined>(undefined);
  // False until the first content settle for a session. On open we land at the bottom (as
  // before) and mark the reply already present as "seen", so only replies that arrive while
  // the user is watching get anchored to the top — history isn't retro-scrolled.
  const didInitRef = useRef(false);

  // --- カラオケ朗読（turnTts, docs/log/24） -----------------------------------------
  // 読み上げ一式（カラオケ・自動読み上げ・作業過程の小声読み・確認の告知・「ここから朗読」）は
  // parts/useMirrorTts へ。リセットの呼び出し口だけがこの下の 2 箇所に残る。
  const tts = useMirrorTts({
    session,
    sessionMeta,
    paneId,
    active,
    readOnly,
    settings,
    bodyRef,
    statusRef,
    loaded,
    pending,
    pendingPlan,
    pendingPerm,
  });
  // 会話へ引いたマーカー（docs/log/69 / ADR 0050）。ここは所有者なので、誰の印でも消せる側。
  // ⚠️ ポーリングは増やさない — 転写のロードに合わせて reload() を呼ぶ（下の effect）。
  const marks = useMarksController({
    path: session ? `api/sessions/${q(session)}/marks` : "",
    canEdit: true,
    isOwner: true,
    viewerId: "",
    ownerLabel: tr("chat.you"),
    youLabel: tr("chat.you"),
  });
  // 転写のポーリング effect は購読を張り直したくないので、最新の reload を ref で渡す。
  const marksReloadRef = useRef(marks.reload);
  marksReloadRef.current = marks.reload;


  // Reset accumulated turns when the session changes (cursor is a line index into
  // that session's jsonl, meaningless across sessions).  This MUST be a layout
  // effect: a pane can keep MirrorView mounted while its session prop changes. A
  // passive effect then leaves the old transcript and its scrolled-up `atBottom`
  // state in place for one paint, so the incoming session can inherit an arbitrary
  // middle position instead of taking its normal initial-bottom path.
  useLayoutEffect(() => {
    setStateSession(session); // …and from the next render on, the state below is this session's
    cursorRef.current = 0;
    firstLineRef.current = 0;
    loadingOlderRef.current = false;
    prependAdjustRef.current = null;
    setHasMore(false);
    setLoadingOlder(false);
    diagRef.current = "";
    statusRef.current = "";
    setTurns([]);
    setPendingSends(echoStore.get(session) ?? []); // restore this session's un-landed echoes
    pendingSendsRef.current = echoStore.get(session) ?? [];
    rejectedPlansRef.current = new Set(); // optimistic 却下 marks belong to the old session
    setLoaded(false);
    setTermState("");
    setStatus("");
    setBgBusy(false);
    setFinalizing(false); // the idle→reply bridge belongs to the old session
    finalizingRef.current = false;
    wasWorkingRef.current = false;
    setTasks([]);
    setFiles([]);
    setQueuedPrompts([]);
    setAlive(!!sessionMeta?.alive);
    setPending(null);
    setPendingPlan(null);
    setPendingPerm(null);
    setMode("");
    lastNonPlanMode.current = "";
    setSuggestedTitle("");
    setTitleActing(false);
    setManagedSettingsOpen(false);
    setManagedSettings(null);
    setHistIdx(null);
    setPasting(false);
    setLightbox(null);
    setAttachments((prev) => {
      prev.forEach((a) => URL.revokeObjectURL(a.url)); // don't leak the old session's previews
      return [];
    });
    atBottomRef.current = true; // a freshly opened session starts pinned to the bottom
    // The old scroller can be reused for another session (pane D&D / opening a row
    // in the current mirror). Clear its physical offset in the same pre-paint phase;
    // the first transcript layout effect below then pins the new content to its end.
    if (bodyRef.current) { bodyRef.current.scrollTop = 0; selfTopRef.current = 0; }
    // 「読者自身が広げた」窓は前のセッションの話なので、持ち越さない。スマホの横スワイプで
    // セッションを持ち替えると、その指の pointerdown が transcript の上に降りて
    // noteInteraction を 600ms 武装する（.mirror-scroll の capture ハンドラ）。ミラーの高さは
    // ほぼ全部が遅れて入るので、窓が開いたままだと下の ResizeObserver の再ピンが握りつぶされ、
    // 着地位置が中途半端なところで止まりうる。
    //
    // ただし正直に言うと、これは塞いだ穴であって再現した不具合ではない: mirror-scroll の
    // swipe シナリオでは、この 1 行の有無にかかわらず末尾に着地した（fetch とレンダが毎回
    // 600ms より長くかかり、窓が閉じたあとの成長で再ピンが効いてしまう）。窓が実際に効く
    // 速さの端末では効く、という理屈のぶんだけの手当て。
    interactUntilRef.current = 0;
    // このセッションを最後に見ていた位置（あれば）。実際に戻すのは transcript が載ってから＝
    // 下の初回 settle で、そこまでは末尾ピンのまま待つ。
    restoreMarkRef.current = loadMark(session);
    restoringRef.current = false;
    setShowJump(false); // …so no jump-to-latest affordance until they scroll up
    setShowReplyTop(false); // 新しいセッションの回答が載るまで頭出しの対象が無い
    anchoredIdxRef.current = undefined; // no reply anchored yet in the new session
    answerAnchoredRef.current = undefined; // …nor its final answer
    didInitRef.current = false; // re-run the "land at bottom on open" settle for this session
    tts.resetForSession(); // 自動読み上げ・小声読み・確認告知の基準を取り直す（履歴は読まない）
    // 離脱時（別セッションへの持ち替え・ターミナルへの切替・ペインを閉じる）に、いま見ていた
    // 位置を控える。cleanup が読む session / DOM は「出ていく側」のもの: React はこの
    // クリーンアップを、新しい props でのレンダを DOM に反映したあと・次の layout effect
    // より前に走らせるが、transcript の中身は state（turns）なので、まだ古いセッションの
    // ターンが載ったままで scrollTop も動いていない。
    return () => {
      saveMark(session, captureMark(bodyRef.current, atBottomRef.current));
    };
  }, [session]);

  // Poll the transcript since our cursor while this view is mounted (Pane only mounts
  // it while visible). Faster while claude is working, slower at rest. New turns are
  // appended; the cursor advances by the transcript's line count.
  useEffect(() => {
    if (!session) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const tick = async () => {
      // Hidden tab: skip the fetch entirely (mobile data / battery); the
      // visibilitychange listener below re-polls immediately on return.
      if (document.hidden) {
        timer = setTimeout(tick, 15000);
        return;
      }
      try {
        // First poll: fetch only the TAIL window (fast on huge transcripts); the server
        // returns firstLine/hasMore so we can page older history in on scroll. Subsequent
        // polls are plain since=<cursor> increments (unchanged).
        const first = cursorRef.current === 0;
        const url = first
          ? `api/sessions/${q(session)}/messages?since=0&tail=1&limit=${WINDOW}`
          : `api/sessions/${q(session)}/messages?since=${cursorRef.current}`;
        const d = await api(url);
        if (!alive) return;
        // 印の取り直しは転写のポーリングに相乗りさせる（新しい周期を作らない）。実際の
        // 往復は useMarksController 側で間引かれる。
        marksReloadRef.current();
        if (d && !d.error) {
          if (typeof d.cursor === "number") cursorRef.current = d.cursor;
          // reset: the server's jsonl shrank or was replaced (compaction, or a
          // different <sid>.jsonl became live), so our line cursor was stale and it
          // re-sent from the top — replace, don't append. Otherwise append new turns.
          // Late interaction answers (AskUserQuestion/ExitPlanMode/Agent), keyed by
          // tool_use id — see patchAnswers. Sent every poll; applied to whatever turns we
          // hold after the append/reset below.
          const answers =
            d.answers && typeof d.answers === "object" ? (d.answers as Record<string, InteractionAnswerWire>) : null;
          if (d.reset) {
            setTurns(patchAnswers(Array.isArray(d.messages) ? d.messages : [], answers));
            // Servers now resend a TAIL window on reset and set firstLine/hasMore
            // (handled by the shared block below); 0/false is the fallback for a
            // whole-file reset from an older server (fork preview still sends one).
            firstLineRef.current = 0;
            setHasMore(false);
            tts.resetForTranscript(); // 本文 DOM の入れ替え（全体停止にはしない）＋基準の取り直し
          } else if (Array.isArray(d.messages) && d.messages.length) {
            // Idempotent merge: normally a poll only appends turns. Store-backed agents
            // (notably OpenCode) also update the parts of their current assistant turn
            // while its stable idx stays the same, so replace that overlapping turn.
            // A quick re-poll after sending can likewise overlap safely.
            setTurns((t) => {
              const byIdx = new Map<number, number>();
              for (let i = 0; i < t.length; i++) {
                if (t[i].idx !== undefined) byIdx.set(t[i].idx as number, i);
              }
              let next = t;
              for (const incoming of d.messages as Turn[]) {
                const at = incoming.idx === undefined ? undefined : byIdx.get(incoming.idx);
                if (at === undefined) {
                  if (next === t) next = [...t];
                  next.push(incoming);
                  if (incoming.idx !== undefined) byIdx.set(incoming.idx, next.length - 1);
                } else if (JSON.stringify(next[at]) !== JSON.stringify(incoming)) {
                  if (next === t) next = [...t];
                  next[at] = incoming;
                }
              }
              return patchAnswers(next, answers);
            });
          } else if (answers) {
            // No new turns this poll, but an answer may have just landed for a question/plan/
            // delegation turn we already hold (its tool_result line carries no displayable turn
            // of its own). Patch in place; patchAnswers no-ops when nothing changed.
            setTurns((t) => patchAnswers(t, answers));
          }
          // Windowed (initial tail) response carries the oldest line we now hold.
          if (typeof d.firstLine === "number") {
            firstLineRef.current = d.firstLine;
            setHasMore(!!d.hasMore);
          }
          // Diagnostic: surface the anomalies behind "sent but nothing shows" — no
          // jsonl found, multiple <sid>.jsonl siblings (a stub may shadow the real
          // log), or a cursor reset. Logged once per distinct situation (not every
          // poll) so it's quiet in the normal case.
          if (d.reset || d.jsonlMatches > 1 || (d.alive && !d.jsonlPath)) {
            const sig = `${d.reset ? 1 : 0}|${d.jsonlPath || ""}|${d.jsonlMatches || 0}`;
            if (sig !== diagRef.current) {
              diagRef.current = sig;
              // eslint-disable-next-line no-console
              console.warn("[mirror] transcript diagnostic", {
                session,
                reset: !!d.reset,
                jsonlPath: d.jsonlPath,
                jsonlLines: d.jsonlLines,
                jsonlMtime: d.jsonlMtime,
                jsonlMatches: d.jsonlMatches,
              });
            }
          }
          if (d.status) {
            statusRef.current = d.status;
            setStatus(d.status);
          }
          // Track liveness so a read-only (history) view can enable its composer the
          // moment a background resume brings the session up.
          setAlive(!!d.alive);
          bgBusyRef.current = !!d.backgroundBusy;
          setBgBusy(!!d.backgroundBusy);
          setBgBusyReason(typeof d.backgroundBusyReason === "string" ? d.backgroundBusyReason : "");
          setTasks(Array.isArray(d.tasks) ? d.tasks : []);
          setFiles(Array.isArray(d.files) ? d.files : []);
          setQueuedPrompts(Array.isArray(d.queuedPrompts) ? d.queuedPrompts : []);
          setPending(Array.isArray(d.pendingQuestions) ? d.pendingQuestions : null);
          setPendingText(typeof d.pendingText === "string" ? d.pendingText : "");
          setPendingPlan(typeof d.pendingPlan === "string" && d.pendingPlan ? d.pendingPlan : null);
          setPendingPerm(typeof d.pendingPermission === "string" && d.pendingPermission ? d.pendingPermission : null);
          setCarried(d.carried && typeof d.carried === "object" ? (d.carried as CarriedInteraction) : null);
          // Mode comes from the terminal (paneMode) in real time, so trust every poll —
          // the optimistic set on click just gives instant feedback until this confirms.
          const nextMode = typeof d.mode === "string" ? d.mode : "";
          // 非 plan のときの実際の名前を覚えておく（plan を抜けたときの楽観ラベル用）。
          // 種別の既定ラベルを使うと、権限確認ありで起動した claude に "Bypass" と出る
          // （docs/log/76）。端末が名乗った値なら、その取り違えが起こらない。
          if (nextMode && nextMode.toLowerCase() !== "plan") lastNonPlanMode.current = nextMode;
          setMode(nextMode);
          setAgentCtx(
            d.context && typeof d.context.tokens === "number" && typeof d.context.window === "number" && d.context.window > 0
              ? { tokens: d.context.tokens, window: d.context.window }
              : null,
          );
          setTermState(typeof d.terminalState === "string" ? d.terminalState : "");
          setCompactProg(
            d.compactProgress && typeof d.compactProgress.pct === "number"
              ? { pct: d.compactProgress.pct, elapsed: d.compactProgress.elapsed }
              : null,
          );
          setSuggestedTitle(typeof d.suggestedTitle === "string" ? d.suggestedTitle : "");
          setLoaded(true); // first (and every) successful fetch: drop the loading spinner
          // Self-heal a 反映待ち echo that can no longer reconcile because the turn it
          // should match never reached us (a cursor handed out past a turn we then never
          // asked for again). Only while the session is at rest — a pending echo is
          // normal and expected mid-turn — and once per echo: rewind the cursor so the
          // next tick re-reads the tail window from scratch, which fills the hole and lets
          // the echo land. If the prompt genuinely never arrived, nothing changes and the
          // badge keeps telling the truth.
          const stuck = pendingSendsRef.current[0];
          if (
            stuck &&
            statusRef.current !== "working" &&
            !bgBusyRef.current &&
            !finalizingRef.current &&
            echoNeedsResync(stuck, Date.now())
          ) {
            cursorRef.current = 0;
            const stampedAt = Date.now();
            applyEchoes((p) => p.map((e) => (e.id === stuck.id ? { ...e, resyncedAt: stampedAt } : e)));
          }
        }
      } catch {
        /* transient; retry on the next tick */
      }
      if (!alive) return;
      timer = setTimeout(tick, statusRef.current === "working" || bgBusyRef.current || finalizingRef.current ? 1200 : 3000);
    };
    tickRef.current = () => {
      if (timer) clearTimeout(timer);
      tick();
    };
    const onVisible = () => {
      if (!document.hidden) tickRef.current?.();
    };
    document.addEventListener("visibilitychange", onVisible);
    tick();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
      tickRef.current = null;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [session]);

  // Reconcile optimistic echoes: once a sent prompt's real user turn lands in the
  // transcript (a matching non-noise user turn; managed attachments also have a unique
  // saved-path fallback),
  // drop the echo so the message isn't shown twice.
  useEffect(() => {
    applyEchoes((prev) => {
      if (!prev.length) return prev;
      const next = prev.filter((e) => !echoLanded(e, turns, isNoise));
      return next.length === prev.length ? prev : next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, status]);

  // Keep the conversation in view as it grows — but ONLY while the user is stuck to the
  // bottom (atBottomRef). If they've scrolled up to read, we never move them.
  //
  // Runs as a LAYOUT effect: it fires synchronously after the DOM mutates but BEFORE the
  // browser paints or dispatches scroll events. That matters at completion, when the work
  // trace folds into a disclosure and the content height suddenly shrinks — reading/
  // scrolling here first means we set a valid scrollTop before the browser would clamp it
  // and fire a stray scroll (which used to race this effect and mis-place the viewport).
  //
  // While a reply is still WORKING we follow the bottom so the streamed 作業過程 / answer stays
  // in view. We do NOT strand the user at the end of a long answer, though: the moment the
  // reply COMPLETES we re-anchor once to the FINAL ANSWER's first line at the viewport top
  // (tracked by its idx), so it reads from the start instead of the tail. That upward scroll
  // honestly flips atBottomRef→false via onBodyScroll, so afterwards the user is left alone.
  useLayoutEffect(() => {
    scheduleReplyTopSync(); // 内容が変わるたび「返信を頭から」の要否を採り直す（末尾追従の有無に依らない）
    if (!atBottomRef.current) return;
    const el = bodyRef.current;
    if (!el) return;
    const toBottom = () => {
      el.scrollTop = el.scrollHeight;
      selfTopRef.current = el.scrollTop;
      atBottomRef.current = true; // authoritative now (don't wait for the async scroll event)
    };

    // Actionable prompts (question / plan / permission) render at the very bottom and need
    // a response — always surface them fully.
    if (pending || pendingPlan || pendingPerm) {
      toBottom();
      return;
    }

    // The reply to the latest user prompt is the first assistant block after the last user
    // turn. Its idx is stable for the whole reply (further streamed turns and any subagent
    // blocks append after it), so a change of idx marks a genuinely new reply.
    let u = -1;
    for (let i = groups.length - 1; i >= 0; i--) {
      if (groups[i].role === "user") { u = i; break; }
    }
    const reply = groups[u + 1];
    const replyIdx = reply && reply.role !== "user" ? reply.idx : undefined;

    // First settle for this session: land at the bottom (the familiar "open shows the
    // latest" position) and remember whatever reply is already there, so history isn't
    // retro-anchored — only replies that arrive while watching get the top treatment below.
    if (!didInitRef.current) {
      if (groups.length || loaded) {
        didInitRef.current = true;
        anchoredIdxRef.current = replyIdx;
        answerAnchoredRef.current = replyIdx; // a reply already present at open isn't re-anchored
        // …ただし、このセッションを「途中まで読んだ状態」で離れていたなら、そこへ戻す
        // （scrollMark）。末尾で離れていた（atBottom）ときは意図が「最新を見る」なので
        // 従来どおり末尾。アンカーのターンが tail ウィンドウに載っていなければ復元は
        // 諦めて末尾＝どのみち読み直せる位置に落とす。
        const mark = restoreMarkRef.current;
        if (mark && !mark.atBottom && applyMark(el, mark)) {
          selfTopRef.current = el.scrollTop;
          atBottomRef.current = false; // 末尾ではない ⇒ 追従は切れ、最新へ の導線が出る
          restoringRef.current = true; // 以後、遅れて入る高さのたびにこのアンカーへ置き直す
          setShowJump(true);
          scheduleReplyTopSync();
          return;
        }
        restoreMarkRef.current = null;
      }
      toBottom();
      return;
    }

    if (replyIdx !== undefined) {
      // Start tracking a newly-arrived reply, but do NOT anchor its top: while it streams we
      // follow the bottom (below) so the user watches progress. answerAnchoredRef resets so the
      // final-answer anchor fires once this reply completes.
      if (replyIdx !== anchoredIdxRef.current) {
        anchoredIdxRef.current = replyIdx;
        answerAnchoredRef.current = undefined; // this reply's final answer hasn't been anchored yet
      }
      // Still working, a background run (サブエージェント/Workflow) is appending, or we're
      // bridging the idle→reply gap (finalizing) — follow the bottom so the streamed tail
      // (and the typing indicator) stay in view.
      if (busy) {
        toBottom();
        return;
      }
      // Completed: a following pane collapses the 作業過程 into a disclosure (defaultWorkOpen=
      // !atBottom, and we've been at the bottom) so the reply's top becomes that collapsed row —
      // re-anchor once to the FINAL ANSWER's first line at the viewport top, so the user reads
      // it from the start rather than the tail we followed to. Only when work was actually
      // folded; a reply with no foldable work already sits with its answer at the top.
      if (answerAnchoredRef.current !== replyIdx) {
        const body = el.querySelector<HTMLElement>(`[data-turn-idx="${replyIdx}"] .mirror-turn-body`);
        const work = body?.querySelector<HTMLElement>(":scope > .mt-work");
        const answer = work?.nextElementSibling as HTMLElement | null;
        if (work && answer) {
          answerAnchoredRef.current = replyIdx;
          const top = el.scrollTop + (answer.getBoundingClientRect().top - el.getBoundingClientRect().top) - 12;
          el.scrollTop = Math.max(0, top);
          selfTopRef.current = el.scrollTop;
          atBottomRef.current = false; // parked at the answer top — leave the user here (and stop the RO re-pin)
        } else if (body && !work) {
          answerAnchoredRef.current = replyIdx; // nothing folded — top already is the answer
        }
      }
      return;
    }

    // No reply yet (the user's own just-sent prompt is the newest thing): keep it in view.
    toBottom();
    // `groups` and `loaded` are derived from / move with the listed deps, so the closure is
    // fresh on every run; keeping them out of the deps avoids re-firing on unrelated
    // re-renders (e.g. every composer keystroke).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, pending, pendingPlan, pendingPerm, status, bgBusy, finalizing, pendingSends, queuedPrompts]);

  // Keep a bottom-stuck view pinned as geometry changes OUTSIDE the poll-driven follow
  // effect: the body's own box resizing (ToDo / 消費推移 / コンテキスト panels above it, the
  // composer auto-growing, a pane/window resize) AND — via the inner wrapper — the
  // transcript's content height changing as late content lays out (images, code
  // highlighting, math) or streams in. This is what makes opening a session settle at the
  // TRUE bottom instead of a stale pre-layout position, and keeps streaming glued to the
  // tail. atBottomRef is authoritative (the follow effect sets it synchronously right after
  // it scrolls), so a completion-anchored view that was scrolled up is left alone.
  useEffect(() => {
    const el = mirrorRef.current;
    if (!el) return;
    const syncHeight = () => el.style.setProperty("--mirror-todo-max-height", el.clientHeight * 0.2 + "px");
    syncHeight();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(syncHeight);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Re-pin whenever the geometry changes while follow is on: the body's own box resizing
  // (ToDo / 消費推移 / コンテキスト panels above it, the composer auto-growing, a pane/window
  // resize) AND — via the inner wrapper — the transcript's content height changing as late
  // content lays out or streams in.
  //
  // There is deliberately NO list of late-layout sources here and no time window. The
  // transcript's body is rendered by MarkdownView into innerHTML from a PASSIVE effect, so
  // at the moment the follow effect pins the bottom the turns are still empty: essentially
  // ALL of a transcript's height arrives late, in several steps (parse → highlight → math →
  // mermaid → image decode → web fonts). Enumerating those sources is what the previous
  // rounds of this fix tried; each new source (and each slow machine) reopened the bug. The
  // rule is simply "while following, keep the end in view".
  //
  // The one growth we must NOT chase is the one the user caused themselves — expanding a
  // 作業過程 disclosure while parked at the bottom must keep their position, not snap them
  // past what they just opened. That is decided by cause, not by timing: interactUntilRef
  // is armed by an interaction inside the transcript (see the handlers on .mirror-scroll).
  useEffect(() => {
    const el = bodyRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      scheduleReplyTopSync();
      // 位置復元中は、末尾ではなくアンカーを保つ。理由は末尾ピンと同じ（高さが遅れて入る）で、
      // 向き先だけが違う。atBottomRef が立っていたら誰かが末尾追従へ戻した合図（送信・最新へ）
      // なので、そちらを優先して復元を畳む。
      if (restoringRef.current) {
        const mark = restoreMarkRef.current;
        if (mark && !atBottomRef.current && applyMark(el, mark)) {
          selfTopRef.current = el.scrollTop;
          return;
        }
        endRestore();
      }
      if (!atBottomRef.current) return; // scrolled up, or parked at a completion anchor
      if (Date.now() < interactUntilRef.current) return; // the reader's own click grew it
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 1) return;
      el.scrollTop = el.scrollHeight;
      selfTopRef.current = el.scrollTop;
    });
    ro.observe(el);
    if (scrollBoxRef.current) ro.observe(scrollBoxRef.current);
    return () => ro.disconnect();
  }, []);

  // Arm the "the reader caused this reflow" window. A pointer or keyboard interaction inside
  // the transcript can toggle a disclosure (作業過程 / thinking / tool run), switch code
  // wrapping, or open a plan comment box — all of which grow the content under a reader who
  // is sitting at the bottom. Both are captured on .mirror-scroll, so the pointer path and
  // the keyboard path (Enter/Space on a <summary>) arm it before the reflow lands. A fold
  // that WE change (foldWork on completion) is content, not interaction, and is followed.
  const noteInteraction = () => {
    interactUntilRef.current = Date.now() + INTERACT_HOLD_MS;
    endRestoreOnInput(); // 読者が触った ⇒ 位置の復元より、その手を優先する
  };

  // Follow state, from user INTENT rather than from raw geometry.
  //
  // The trap this replaces: a scroll EVENT is dispatched asynchronously, so by the time the
  // handler runs the content may have grown past the offset we ourselves just pinned.
  // Measuring "distance to the bottom" at that point reads our own pin as "the user scrolled
  // up", drops follow, and thereby disarms every re-pin path above — the view then stays
  // wherever the last late layout left it (measured: 754→1246px above the end when opening
  // a long transcript). Only an actual UPWARD move relative to the offset we last wrote is
  // the user. Comparing positions is not the old "suppress the next event" dance: nothing is
  // skipped, so the flag cannot drift out of sync with a scroll we never hear about.
  const onBodyScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    scheduleReplyTopSync();
    const movedUp = el.scrollTop < selfTopRef.current - 1;
    if (atBottomRef.current && !movedUp) {
      // Following, and the viewport did not move up — the gap (if any) is content that grew
      // after our pin, and the ResizeObserver above closes it. Stay armed.
      selfTopRef.current = el.scrollTop;
      setShowJump((s) => (s === false ? s : false));
      return;
    }
    // Either the user moved up (drop follow), or they are scrolling back down (re-arm once
    // they are within NEAR_BOTTOM_PX of the end).
    const stuck = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    atBottomRef.current = stuck;
    if (stuck) selfTopRef.current = el.scrollTop;
    setShowJump((s) => (s === !stuck ? s : !stuck));
  };

  // 位置復元をやめる（ユーザーが触った／末尾追従へ戻った）。以後は従来どおり atBottomRef だけが
  // 追従を決める。
  const endRestore = () => {
    restoringRef.current = false;
    restoreMarkRef.current = null;
  };

  // 復元を打ち切るのは「ユーザーが入力した」ときだけ — scrollTop が自分の書いた値からズレた
  // ことを根拠にしてはいけない。ブラウザ自身のスクロールアンカリング（上の内容が伸びた分だけ
  // scrollTop を勝手に足して見た目を保つ機構）が遅延レイアウトのたびに動かすので、それを
  // 「触られた」と読むと復元を途中でやめてしまう（実測: 目的地の 354px 手前で固まり、以後
  // 二度と直らなかった）。入力（ホイール・タッチ・キー・ポインタ）だけを退出条件にすれば、
  // アンカリングのズレは次の再適用で必ず上書きされる。
  //
  // 取りこぼすのはネイティブのスクロールバーをドラッグした場合（Chromium は要素へ
  // pointerdown を出さない）。復元が畳まれるまで引っぱり合いになるが、掴み直せば済む。
  const endRestoreOnInput = () => {
    if (restoringRef.current) endRestore();
  };

  // Jump-to-latest button: snap to the bottom and re-arm auto-follow.
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    endRestore(); // 明示的に末尾を選んだ ⇒ 復元アンカーは捨てる
    el.scrollTop = el.scrollHeight;
    selfTopRef.current = el.scrollTop;
    atBottomRef.current = true;
    setShowJump(false);
    syncReplyTop();
  };

  // 「返信を頭から」— 最新の回答ブロックの上端を画面の一番上へ。長い回答の途中から 1 タップで
  // 頭出しするための導線（末尾に貼り付いている間は出さない — syncReplyTop の注記）。
  //
  // 対象はユーザー発言ではなく回答ブロックの先頭（＝畳まれた 作業過程 の行から）。完了時の
  // 自動アンカー（answerAnchoredRef、回答本文の 1 行目）より 1 段上を見せる位置で、「この
  // 返信は何をやったのか」から読み直せる。
  const jumpToReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    if (!el || idx === undefined) return;
    const top = scrollTopForTurn(el, idx, REPLY_TOP_PAD);
    if (top === null) return;
    endRestore();
    el.scrollTop = top;
    selfTopRef.current = el.scrollTop;
    // 末尾から離れた ⇒ 追従は切る（ここで切らないと、次の poll でまた末尾へ引き戻される）。
    atBottomRef.current = false;
    setShowJump(true);
    syncReplyTop();
  };

  // ピルの出し入れ（＝下の setState）は、必ず次のフレームへ逃がす。末尾ピンと同じフレームで
  // DOM を足し引きすると着地を壊す — 実測: ResizeObserver や follow の layout effect から
  // 直接呼んだ版は、末尾着地が 4 回に 1 回ほど 240px（＝画像 1 枚ぶんの遅延レイアウト）手前で
  // 止まり、そのまま直らなかった。mirror-scroll ハーネスの long シナリオが赤くなる。
  // 1 フレーム遅れて出ることに実害はないので、素直に逃がす。
  const replyTopSyncRef = useRef(false);
  const scheduleReplyTopSync = () => {
    if (replyTopSyncRef.current) return;
    replyTopSyncRef.current = true;
    requestAnimationFrame(() => {
      replyTopSyncRef.current = false;
      syncReplyTop();
    });
  };

  // 「返信を頭から」を出すべきか — 最新の回答ブロックの先頭が、ビューポート上端より上に
  // 流れているときだけ。すでに頭が見えているなら押しても何も起きないので出さない。
  const syncReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    const turn = el && idx !== undefined ? el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`) : null;
    const on = !!(
      el &&
      turn &&
      // 末尾に貼り付いている間は出さない。末尾には押すべきものが並ぶ面（引き継ぎカードの
      // 起動ボタン、質問 / プラン / 許可の回答ボタン、コピー…）で、その上に浮くピルが被って
      // 押せなくなる。読んでいる途中＝追従が切れているときだけの導線にする。
      !atBottomRef.current &&
      turn.getBoundingClientRect().top < el.getBoundingClientRect().top - REPLY_TOP_PAD
    );
    setShowReplyTop((s) => (s === on ? s : on));
  };

  // Page older history in (P2): fetch the window before the oldest line we hold and
  // prepend it. Guard via refs so overlapping triggers (button + observer) can't double it.
  const loadOlder = async () => {
    if (loadingOlderRef.current || firstLineRef.current <= 0) return;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    try {
      const before = firstLineRef.current;
      const d = await api(`api/sessions/${q(session)}/messages?before=${before}&limit=${WINDOW}`);
      if (d && !d.error && Array.isArray(d.messages)) {
        if (d.messages.length) {
          const el = bodyRef.current;
          prependAdjustRef.current = el ? el.scrollHeight : null; // pin the viewport across the prepend
          const older = d.messages;
          setTurns((t) => [...older, ...t]);
        }
        if (typeof d.firstLine === "number") firstLineRef.current = d.firstLine;
        setHasMore(!!d.hasMore);
      }
    } catch {
      /* transient — the user can trigger again */
    } finally {
      loadingOlderRef.current = false;
      setLoadingOlder(false);
    }
  };

  // After an older page is prepended, restore the viewport: scrollTop grows by exactly the
  // height added on top, so the user stays on the same content instead of jumping up.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (el && prependAdjustRef.current != null) {
      el.scrollTop += el.scrollHeight - prependAdjustRef.current;
      selfTopRef.current = el.scrollTop;
      prependAdjustRef.current = null;
    }
  }, [turns]);

  // Auto-load older history when the top sentinel scrolls into view (prefetch a little
  // early via rootMargin). Only active while there's more above.
  useEffect(() => {
    const el = topSentinelRef.current;
    const root = bodyRef.current;
    if (!el || !root || !hasMore) return;
    const ob = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadOlder();
      },
      { root, rootMargin: "240px 0px 0px 0px" },
    );
    ob.observe(el);
    return () => ob.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasMore, session]);

  // Auto-grow the composer to fit its content (up to ~10 lines via the CSS max-height,
  // then it scrolls). Runs on every draft change, including the per-session draft restored
  // on mount. 計測のために一瞬 2 行へ縮めると、その隙に転写（この入力欄の兄弟）の
  // clientHeight が伸びて末尾に貼りついていた scrollTop が切り詰められる — 縮みを外へ
  // 出さないのは autoGrowTextarea の仕事（lib/autoGrow.ts の注記）。
  useEffect(() => {
    autoGrowTextarea(inputRef.current);
  }, [draft]);

  // Focus the composer when this pane becomes the active chat — but not on touch
  // devices, where auto-focus would pop the on-screen keyboard just from switching
  // to read the chat. There the user taps the composer to type. (The other focus
  // calls below are keystroke-driven — send / history nav — so the keyboard is
  // already up and refocusing is fine.)
  useEffect(() => {
    if (active && !coarsePointer()) inputRef.current?.focus();
  }, [active]);

  // After the user hits 再開して続ける, focus the composer the moment it becomes usable
  // (readOnly clears + the session goes alive + no resume menu in the terminal), so they
  // can type straight away. Flag is set on the resume click so this fires only for a
  // user-initiated resume, not a background one.
  const wantResumeFocusRef = useRef(false);
  useEffect(() => {
    if (wantResumeFocusRef.current && !readOnly && alive && termState !== "resume") {
      wantResumeFocusRef.current = false;
      inputRef.current?.focus();
    }
  }, [readOnly, alive, termState]);

  // Low-level: submit one prompt as a semantic turn op — start when idle, steer when a
  // turn is already running (docs/log/27 §4). The Agent adapts it per driver: tui = the same
  // tmux typing as before (sessionTurn falls back to /input against an old Agent),
  // managed = the turn/start・turn/steer RPC (P2). The result carries the rejection
  // reason so the caller can drop its optimistic echo AND tell the user why.
  // attachments は managed だけが渡す（driver が API 添付へ変換、docs/log/27 §10.2-3）。
  const postInput = (text: string, op: "start" | "steer", attachments?: string[]): Promise<TurnResult> =>
    sessionTurn(session, op, text, attachments);

  // wsDown: the workspace isn't running, so nothing can receive an agent-bound action —
  // it would just 502, and helpers that optimistically flip the UI to "working" would leave
  // that spinner stuck (the poll is frozen while stopped). Every send helper funnels through
  // here: the live composer is already hidden while stopped (the !running branch below), but
  // the pending 許可/質問/プラン cards and the 停止 button render OUTSIDE that branch, so each
  // must self-guard. Returns true (and toasts once) when the action must be dropped.
  const wsDown = (): boolean => {
    if (running) return false;
    toast(tr("mirror.ws_stopped"));
    return true;
  };

  // Low-level: send named keys with NO "working" status and NO quick re-poll — used by
  // the plan-mode toggle, which isn't a turn. (The quick re-poll of sendKeys/sendPrompt
  // would fire before the mode actually changed and momentarily revert the optimistic
  // indicator; the regular poll picks up the real mode via paneMode.)
  const postKeys = async (keys: string[]) => {
    if (wsDown()) return; // plan-mode toggle / codex update-menu skip: no agent to key while stopped
    try {
      await apiJSON(`api/sessions/${q(session)}/input`, "POST", { keys });
    } catch {
      /* next poll reconciles */
    }
  };

  // Newest real (jsonl-backed) turn idx currently held, or -1. Used to anchor an
  // optimistic echo so it only reconciles against a turn that arrives after the send.
  const newestIdx = (): number => {
    for (let i = turns.length - 1; i >= 0; i--) {
      if (turns[i].idx !== undefined) return turns[i].idx as number;
    }
    return -1;
  };

  // sendPrompt submits one prompt (the composer). Never used to answer an AUQ —
  // the modal ignores typed text, so a text send would confirm option 1 (docs/build/92).
  // attachments は managed セッションの API 添付（send() が織り込みと使い分ける）。
  // 戻り値は「セッションに受理されたか」。呼び出し側の大半は投げっぱなしでよいが、
  // プランコメントの送信済みマークだけはこれを見る必要がある — 失敗をトーストするだけで
  // void を返していたころ、届かなかったコメントまで畳まれて打ち直せなくなっていた
  // （permission_pending で弾かれた直後に「送信済み」になる、2026-08-10 報告の症状）。
  const sendPrompt = async (text: string, attachments?: string[]): Promise<boolean> => {
    const t = (text || "").trim();
    // sendingRef (not the `sending` state alone) guards re-entrancy: two invocations
    // arriving in the same task both read `sending` before either commit lands, but the
    // ref is set synchronously right here, so the second call sees it immediately.
    if ((!t && !attachments?.length) || sendingRef.current) return false;
    // WS down: nothing can receive the prompt (a send would 502). The composer is already
    // hidden while stopped, but other callers (seed prompt, file drop) reach here too — bail
    // before the optimistic echo so a send never looks accepted when it can't be.
    if (wsDown()) return false;
    sendingRef.current = true;
    setSending(true);
    // start = 新しい turn / steer = 実行中 turn への追撃 — 楽観的に working へ倒す前の
    // 実状態で決める。tui では同じ型付けに落ちるが、managed の turn/start・turn/steer
    // （P2）にはこの区別がそのまま効く。
    const op = statusRef.current === "working" ? "steer" : "start";
    statusRef.current = "working";
    setStatus("working");
    // Sending is an explicit "take me to the conversation": re-arm auto-follow so the
    // optimistic echo below and the incoming reply are surfaced, even if the user had
    // scrolled up to read history.
    atBottomRef.current = true;
    setShowJump(false);
    // Show the message immediately (optimistic echo) so it never looks lost while claude
    // is busy — reconciled away once its real user turn appears in the transcript.
    const echoId = nextEchoId();
    applyEchoes((p) => [...p, { id: echoId, text: t, sinceIdx: newestIdx(), attachmentPaths: attachments, at: Date.now() }]);
    const res = await postInput(t, op, attachments);
    if (!res.ok) {
      // 送信は受理されていない: echo を残すと「送れたように見える」ので消し、理由を
      // トーストで示し、（send() が既に消した）下書きを書き戻す。ユーザーが打ち直しを
      // 始めていたらそれを潰さない。
      applyEchoes((p) => p.filter((e) => e.id !== echoId));
      toast(res.message || tr("mirror.send_failed"));
      setDraft((d) => d || t);
    }
    sendingRef.current = false;
    setSending(false);
    // Pick up the just-logged user turn quickly rather than waiting a full interval.
    setTimeout(() => tickRef.current?.(), 250);
    return res.ok;
  };

  // Launch seed: a session started from 作業を始める carries a first prompt. The mirror no
  // longer SENDS it — the Agent does, from the create call's initial_prompt (or /input
  // {when_ready} when attachments made the text final only after create; useStartWork.ts).
  // That matters because this view is mounted only while its tab is the selected one:
  // typing it from here meant a session launched into a background tab sat idle until the
  // user came back to it, and then looked as if opening the tab is what sent the message.
  //
  // What is left here is display: show the sent text as an optimistic echo so the chat
  // isn't empty for the seconds between 起動 and the first turn reaching the transcript.
  // It is dropped by the normal reconciliation once that turn lands.
  //
  // sinceIdx is -1 on purpose. An echo's anchor exists to keep it from matching a turn
  // that predates the send, but this one can only ever match the first turn of a brand-new
  // session — while an anchor taken from newestIdx() would strand it forever whenever the
  // turn is ALREADY in the transcript (delivery won the race, or the pane was opened
  // later). stateSession likewise: on the commit where the `session` prop changes this
  // component still holds the PREVIOUS session's state (the reset below is a layout effect,
  // so it lands one render later), and appending an echo there would both anchor it against
  // a foreign transcript and copy the old session's pending echoes into the new one's stash.
  const seededRef = useRef(false);
  useEffect(() => {
    seededRef.current = false; // new session → allow its own seed
  }, [session]);
  useEffect(() => {
    if (seededRef.current || stateSession !== session) return;
    const seed = takeLaunchSeed(session);
    if (!seed) return;
    seededRef.current = true;
    const echoId = nextEchoId();
    applyEchoes((p) => [...p, { id: echoId, text: seed.trim(), sinceIdx: -1, at: Date.now() }]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, stateSession]);

  // driveInput posts one modal-driving body ({keys} or {seq}) and — this is the point —
  // does NOT swallow a rejection. api() resolves non-2xx as a value ({error:{code}}), so
  // the old `try/await/catch {}` here never even ran its catch: a 400 (bad_key, view-nav
  // guard, rate-limit modal) left the card sitting there with no keystroke delivered and
  // no message, i.e. "ボタンを押しても無反応". Answering is the one place where silence is
  // indistinguishable from success, so failures speak — same treatment as sendRespond's
  // managed path. The optimistic 'working' is rolled back too, or the chip claims a turn
  // that never started until the next poll.
  const driveInput = async (body: { keys?: string[]; seq?: Array<{ k?: string; t?: string }> }) => {
    if (sending) return;
    if (wsDown()) return; // WS stopped: no agent to receive the keys
    const prev = statusRef.current;
    setSending(true);
    statusRef.current = "working";
    setStatus("working");
    const res = await apiJSON(`api/sessions/${q(session)}/input`, "POST", body).catch(() => null);
    if (!res || res.error) {
      statusRef.current = prev;
      setStatus(prev);
      toast(res?.error ? errText(res.error) : tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
  };

  // sendKeys drives the AskUserQuestion modal via named keys (Down/Space/Enter), the
  // only way to answer multi-select / multi-question forms (free text can't).
  const sendKeys = async (keys: string[]) => {
    if (!keys || !keys.length) return;
    await driveInput({ keys });
  };

  // sendSeq drives the modal with an ORDERED mix of named keys and literal text — the
  // path for answering a question via its "Type something" free-text row (move down to
  // it, type, Enter). Built by PendingQuestions.submit for multi-question / multi-select
  // forms where free text and option navigation are interleaved.
  const sendSeq = async (seq: Array<{ k?: string; t?: string }>) => {
    if (!seq || !seq.length) return;
    await driveInput({ seq });
  };

  // sendInterrupt stops the running turn — turn/interrupt 相当。tui では Escape に
  // 落ちる（opencode のサブエージェント詳細ビュー特例は /turn のサーバ側が面倒を
  // 見る）。停止ボタンは working 中か BG 実行中しか出ず、いずれも次ポーリングが実状態へ
  // 再同期するので楽観的な状態変更は不要。
  const sendInterrupt = async () => {
    if (sending) return;
    if (wsDown()) return; // WS stopped: no live turn to interrupt (also plan-reject / question-cancel)
    // An explicit stop (also plan-reject / question-cancel) means the user does NOT expect
    // a reply to render, so disarm the idle→reply bridge — otherwise the spinner would
    // linger over an interrupted, reply-less turn until the grace lapsed.
    wasWorkingRef.current = false;
    finalizingRef.current = false;
    setFinalizing(false);
    setSending(true);
    const res = await sessionTurn(session, "interrupt");
    if (!res.ok) toast(res.message || tr("mirror.stop_failed"));
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
  };

  // sendRespond answers a MANAGED session's pending question by interaction id —
  // 構造化回答（docs/log/27 §5）。tui の質問は従来どおり sendKeys/sendSeq で TUI モーダル
  // をナビゲーション駆動する（サーバも tui への /respond は受け付けない）。
  const sendRespond = async (id: string, answers: InteractionAnswer[]) => {
    if (sending) return;
    if (wsDown()) return; // WS stopped: the managed session's structured answer can't be delivered
    setSending(true);
    const prev = statusRef.current;
    statusRef.current = "working";
    setStatus("working");
    const res = await sessionRespond(session, id, answers).catch((): TurnResult => ({ ok: false }));
    if (!res.ok) {
      // 却下（id 不明・driver 未実装・通信断）を握りつぶさない: 状態を戻して質問
      // カードを生かしたまま、理由（あれば）を示す。次ポーリングが実状態へ再同期する。
      statusRef.current = prev;
      setStatus(prev);
      toast(res.message || tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
  };

  // addFiles uploads files to the session and holds each as an attachment chip —
  // shared by clipboard paste, drag&drop onto the pane, and the ＋ picker. Upload +
  // saved path referenced in the prompt (kind-worded by buildImagePrompt).
  const addFiles = async (files: File[]) => {
    if (!files.length) return;
    setPasting(true);
    for (const f of files) {
      try {
        const res = await pasteImage(session, f);
        if (res.status < 300 && res.path && res.name) {
          const path = res.path;
          const nm = res.name;
          const image = f.type.startsWith("image/");
          // Non-images get no preview URL — the chip shows an icon + name instead.
          const url = image ? URL.createObjectURL(f) : "";
          setAttachments((a) => [...a, { path, name: nm, url, image }]);
        } else {
          toast(res.error ? errText(res.error) : tr("mirror.attach_failed"));
        }
      } catch {
        toast(tr("mirror.attach_failed_net"));
      }
    }
    setPasting(false);
    inputRef.current?.focus();
  };

  // Paste file(s) from the clipboard into the composer. Non-file pastes fall through
  // to the default (text). Agents without the cap let everything fall through.
  const onPaste = async (e: RClipboardEvent<HTMLTextAreaElement>) => {
    if (!canPasteImage) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === "file") {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (!files.length) return; // ordinary text paste — let it happen
    e.preventDefault();
    await addFiles(files);
  };

  const removeAttachment = (i: number) => {
    setAttachments((a) => {
      if (a[i]) URL.revokeObjectURL(a[i].url);
      return a.filter((_, idx) => idx !== i);
    });
  };
  const clearAttachments = () => {
    setAttachments((a) => {
      a.forEach((x) => URL.revokeObjectURL(x.url));
      return [];
    });
  };

  // An AskUserQuestion can't be answered by the composer's free text — verified against
  // the terminal (v2.1.204, docs/build/92): the modal IGNORES typed text on option rows
  // entirely (the older "option filter" behavior is gone), so the trailing Enter just
  // confirms the highlighted (first) option — a silent wrong answer. Digit keys 1-9 even
  // select-and-submit instantly, so stray text is doubly dangerous. Lock the composer for
  // ANY pending question and steer the user to the card — its options key-drive the modal
  // (Down×i, Enter) and its "または自由入力" row uses the still-working "Type something" path.
  // 空配列（質問なし）ではロックしない — カードは pending.length > 0 でしか出ないので、
  // !!pending だけだとカード無しのままコンポーザだけ死ぬ。
  const auqLocksComposer = !!pending?.length;
  // A pending plan approval or permission prompt is a menu decision, NOT a free-text turn:
  // sending would type text + Enter, and that Enter selects the menu's default (= 承認 /
  // 許可), silently confirming it. A mode toggle would likewise mis-key the menu. So lock
  // the composer AND the mode chip while one is pending; act via the card's buttons.
  const decisionPending = !!pendingPlan || !!pendingPerm;
  const composerLocked = auqLocksComposer || decisionPending;

  // OS drag&drop anywhere on the pane attaches the dropped files (the composer is a
  // small target — the whole chat area accepts). dragenter/leave nest per child, so a
  // depth counter drives the highlight; drop is ignored while the composer is hidden
  // (read-only history) or locked.
  const canDropFiles = canPasteImage && !readOnly && !composerLocked;
  // A memo dragged from the left-pane queue drops its text into the composer — but ONLY
  // when this session is 入力待ち (alive and idle: not working, no lingering background run,
  // not mid-finalize, composer not locked by an AUQ/plan). A busy session would just queue
  // the text unseen, so we refuse the drop there.
  const sessionIdle = alive && !readOnly && !composerLocked && !busy;
  const canDropMemo = sessionIdle;
  // Which kind of drop, if any, this drag offers here (types are readable on enter/over;
  // getData is not, so the branch is decided from the type list).
  const dragIntent = (e: RDragEvent): "file" | "memo" | null => {
    const types = e.dataTransfer?.types;
    if (!types) return null;
    if (canDropFiles && types.includes("Files")) return "file";
    if (canDropMemo && types.includes(MEMO_DND_MIME)) return "memo";
    return null;
  };
  const onDragEnter = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
    dragDepth.current++;
    setDragging(true);
  };
  const onDragOver = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
  };
  const onDragLeave = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
    if (--dragDepth.current <= 0) {
      dragDepth.current = 0;
      setDragging(false);
    }
  };
  const onDrop = async (e: RDragEvent) => {
    const intent = dragIntent(e);
    if (!intent) return;
    e.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (intent === "memo") {
      const text = e.dataTransfer.getData(MEMO_DND_MIME);
      if (text) insertMemoText(text); // the memo stays queued — this is a copy
      return;
    }
    const files = Array.from(e.dataTransfer?.files || []);
    await addFiles(files);
  };

  // Drop a dragged memo's text into the composer: append below any existing draft, then
  // focus and park the caret at the end. Never sends — the user reviews and submits.
  const insertMemoText = (text: string) => {
    setDraft((d) => (d ? d.replace(/\s*$/, "") + "\n" + text : text));
    setHistIdx(null);
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  };

  // override が来たらそのテキストを送る（サジェストチップの⌥即送信）。無ければコンポーサーの draft。
  const send = async (override?: string) => {
    if (composerLocked) return;
    const text = (override ?? draft).trim();
    if (!text && !attachments.length) return;
    // 短い純テキストは返信サジェストの学習に取り込む（send 経由のみ＝AUQ/plan 応答は自然に除外）。
    // 一度メニューから消した文でも、自分で送り直したなら「また使う」意思表示なので隠しを解除する。
    if (text && isQuickReplyCandidate(text, attachments.length > 0)) {
      setSetting("quickReplies", recordQuickReply(settings.quickReplies || {}, text, Date.now()));
      const hidden = settings.quickRepliesHidden || [];
      const unhidden = unhideQuickReply(hidden, text);
      if (unhidden !== hidden) setSetting("quickRepliesHidden", unhidden);
    }
    // このセッションを読み上げている最中にコンポーサーから送信したら、その読み上げを止める。
    // 割り込み・追撃の意思なので、今さら古い回答（カラオケ・要約アナウンス）を聞かされても
    // 混乱するだけ。sessionName で判定し、他セッション由来の再生（不一致）はそのまま流す。
    const ts = useTtsStore.getState();
    if (ts.active && ts.sessionName === session) ts.stop();
    const paths = attachments.map((a) => a.path);
    // managed はワイヤの attachments で渡し（driver が API 添付へ変換、docs/log/27
    // §10.2-3）、tui は従来どおりプロンプト本文へパスを織り込む。
    const prompt = managed ? text : buildImagePrompt(text, paths, agent.id);
    setHistIdx(null);
    setDraft("");
    clearAttachments();
    // On touch devices, drop focus so the soft keyboard (GBoard) retracts once the
    // turn is sent — the reply is what the user wants to read, not keep typing. Desktop
    // keeps focus (and refocuses below) so typing the next turn needs no extra click.
    if (coarsePointer()) inputRef.current?.blur();
    await sendPrompt(prompt, managed ? paths : undefined);
    if (!coarsePointer()) inputRef.current?.focus();
  };


  // --- スキルピッカー（docs/log/50） --- 本体は parts/useSkillPicker（composerLocked を
  // 見るのでここで呼ぶ）。
  const skillPicker = useSkillPicker({
    session,
    agent,
    managed,
    draft,
    setDraft,
    setHistIdx,
    inputRef,
    composerLocked,
  });


  // Open a plan's Markdown in its own pane (manual — via a button, not automatic).
  // The pane carries docSession so it becomes a REVIEW surface (select → comment);
  // the comments are keyed by session + plan text, which is what makes the card below
  // able to collect them again.
  //
  // Why not plain showDoc: doc panes are identified by TITLE alone (layout/ops
  // sameTarget), so a revised plan re-presented under the same heading would just
  // FOCUS the pane still showing the OLD text — and comments would then be written
  // against text the agent no longer proposes. Replace the content of an already-open
  // plan pane for this session instead, and fall back to opening a new one.
  const openPlan = (plan: string) => {
    const target = {
      content: { kind: "doc" as const, docTitle: planTitle(plan), docContent: plan, docSession: session },
    };
    const open = findPlanPane(session);
    if (open) {
      setPaneTarget(open, target);
      setActivePane(open);
      return;
    }
    openTargetInNew(target);
  };

  // プランへのコメントを送れない理由（"" = 送れる）。コンポーザは停止中に丸ごと消えるが、
  // プランカードは履歴として残り続けるので、送信ボタンだけは自前で塞ぐ必要がある。
  // 停止中でもコメントを溜めること自体は妨げない（再開してから送れる）。
  const planSendBlocked = !running
    ? tr("mirror.ws_stopped")
    : !alive || readOnly
      ? tr("plan.send_needs_running")
      : "";

  // 却下 → 修正 → 再提示で本文が差し替わったら、開いているレビュー面も追従させる。
  // これが無いと、利用者は古い本文を読みながらコメントを付け、送ったあとで「その記述は
  // もう無い」ことに気づく（doc ペインはスナップショットなので黙って古いまま残る）。
  useEffect(() => {
    if (!pendingPlan) return;
    const id = findPlanPane(session);
    if (!id) return;
    const pane = findPane(id);
    if (pane?.content.kind !== "doc" || pane.content.docContent === pendingPlan) return;
    setPaneTarget(id, {
      content: { kind: "doc", docTitle: planTitle(pendingPlan), docContent: pendingPlan, docSession: session },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingPlan, session]);

  // プランへのコメントを届ける。どの経路で送りいつ送信済みにするかの判断は
  // deliverPlanComments（planComments.ts）が持つ — この巨大コンポーネントは
  // レンダリングテストを持てないので、判断だけ外に出して単体で固定してある。
  // ここに残るのは React 側の後始末: 押下ガード、楽観 却下 バッジ、トースト、エコー。
  const sendPlanComments = async (plan: string) => {
    if (sending) return;
    // 停止中のセッションには届かない。プランカードは履歴にも出る＝コンポーザと違って
    // 「停止中は隠れる」という保護が効かないので、ここで明示的に止める（ボタンも
    // planSendBlocked で無効化してあるが、押下と停止が競っても届かないよう二重にする）。
    if (planSendBlocked) {
      toast(planSendBlocked);
      return;
    }
    if (wsDown()) return;
    const isPending = !!pendingPlan && pendingPlan.trim() === plan.trim();
    if (isPending) {
      // 却下を伴う経路。送信ボタンを塞ぎ、バッジと「返信待ち」状態を先に倒しておく
      // （sendPrompt は自前で sending を面倒みるので、発話だけの経路では触らない）。
      setSending(true);
      rejectedPlansRef.current.add(plan.trim()); // 楽観 却下 バッジ（実 outcome で planOutcome が調停）
      wasWorkingRef.current = false; // 中断と同じ: 返信を待つ状態ではなくなる
      finalizingRef.current = false;
      setFinalizing(false);
    }
    const res = await deliverPlanComments(planKey(session, plan), {
      pending: isPending,
      respond: (feedback) => sessionPlanRespond(session, "reject", feedback),
      say: (feedback) => sendPrompt(feedback),
    });
    if (isPending) setSending(false);
    if (!res) return; // 送るものが無い
    if (!res.ok) {
      // 届かなかった＝コメントは畳まれていない。理由を出して打ち直せることを伝える
      // （undelivered = 却下は通ったが本文が入らなかった）。ただし say 経路の失敗は
      // sendPrompt が具体的な理由（許可待ち・停止中…）を既に出しているので重ねない —
      // 汎用の「送信できませんでした」がその上に乗ると、何が起きたのか分からなくなる。
      if (res.reason !== "say") {
        toast(res.message || tr(res.reason === "undelivered" ? "plan.feedback_undelivered" : "mirror.send_failed"));
      }
      return;
    }
    if (res.via === "reject") {
      const echoId = nextEchoId(); // 実ターンが載るまでの楽観エコー（sendPrompt と同じ）
      applyEchoes((p) => [...p, { id: echoId, text: res.feedback, sinceIdx: newestIdx(), at: Date.now() }]);
      setTimeout(() => tickRef.current?.(), 400);
    }
  };

  // Open a SendUserFile entry in its own split pane (same as the file tree's split-open).
  const openFile = (path: string, line?: number, column?: number) =>
    openTargetInNew(
      { content: { kind: "file", filePath: path, targetLine: line, targetColumn: column } },
      true,
    );

  // Open an edit trace's captured before/after in a diff pane. The mirror HAS panes, so
  // it must pass this capability: without it ToolTrace silently takes the degraded path
  // meant for the pane-less shared view (transcript/capabilities.ts) and an edit becomes
  // an inline expansion with nothing to open — which is how it behaved until docs/log/68.
  const openDiff = (p: Part) => {
    const title = p.file ? p.file.split("/").pop() || p.file : p.tool || tr("view.diff");
    const target = { content: { kind: "diff" as const, docTitle: title, diffTool: p.tool || "", diffEdits: p.edits || [] } };
    const open = findDiffPane();
    if (open) {
      setPaneTarget(open, target);
      setActivePane(open);
      return;
    }
    openTargetInNew(target, true);
  };

  // Publish the edited-file list for readers outside this pane (the command palette's
  // このセッションの変更 mode), so they don't have to poll the transcript themselves.
  useEffect(() => {
    if (session) useSessionFilesStore.getState().set(session, files);
  }, [session, files]);

  // Auto-suggested title (session_title.go): 採用 promotes it to the session's real
  // title (bumpSessions so the left-pane label updates without waiting for its own
  // poll); 却下 discards it. Either way the server never offers one again.
  const acceptTitle = async () => {
    if (!session || titleActing) return;
    if (wsDown()) return; // title accept/dismiss is agent-served (session_title.go) → 502 while stopped
    setTitleActing(true);
    try {
      const res = await raw(`api/sessions/${q(session)}/title/accept`, { method: "POST" });
      if (res.ok) {
        setSuggestedTitle("");
        bumpSessions();
      }
    } catch {
      /* transient — next poll re-syncs suggestedTitle either way */
    } finally {
      setTitleActing(false);
    }
  };
  const dismissTitle = async () => {
    if (!session || titleActing) return;
    if (wsDown()) return;
    setTitleActing(true);
    try {
      const res = await raw(`api/sessions/${q(session)}/title/dismiss`, { method: "POST" });
      if (res.ok) setSuggestedTitle("");
    } catch {
      /* same as above */
    } finally {
      setTitleActing(false);
    }
  };
  // Composer history = the user's own prompts in this conversation (so ↑ works even
  // after a reload, not just for prompts typed since mount). Newest last. Slash-command /
  // skill invocations are logged as system-tagged turns that isNoise hides from the transcript
  // view, so they're recovered via parseCommand and pushed in their re-typeable "/name args"
  // form — otherwise a skill run would vanish from ↑ recall entirely.
  const history: string[] = [];
  for (const t of turns) {
    if (t.role !== "user") continue;
    const slash = parseCommand(t);
    const s = slash ? slash.name + (slash.args ? " " + slash.args : "") : t.text && !isNoise(t) ? t.text.trim() : "";
    if (s && history[history.length - 1] !== s) history.push(s);
  }

  // Recall the previous / next prompt from history (shared by ↑/↓ and the on-screen
  // buttons shown on phones, which have no arrow keys).
  const recallPrev = () => {
    if (!history.length) return;
    const ni = histIdx !== null ? Math.max(0, histIdx - 1) : history.length - 1;
    setHistIdx(ni);
    setDraft(history[ni]);
    inputRef.current?.focus();
  };
  const recallNext = () => {
    if (histIdx === null) return;
    const ni = histIdx + 1;
    if (ni >= history.length) {
      setHistIdx(null);
      setDraft("");
    } else {
      setHistIdx(ni);
      setDraft(history[ni]);
    }
    inputRef.current?.focus();
  };


  const onKeyDown = (e: RKeyboardEvent) => {
    if (skillPicker.handleKeyDown(e)) return; // スキルピッカーが開いていれば ↑↓/Enter/Tab/Esc を横取り
    if (suggest.handleKeyDown(e)) return; // Tab: チップ行への入場 / 補完サイクル
    // Scroll the transcript without leaving the composer: Ctrl/⌘+↑/↓ nudges, PageUp/PageDown
    // (and Ctrl/⌘+[ / ]) page, Ctrl/⌘+End snaps to the newest turn and re-arms auto-follow.
    // Checked before history recall so the modified arrows don't get swallowed by the ↑/↓
    // recall path below.
    if (!e.nativeEvent.isComposing && scrollComposerViewport(e, bodyRef.current, jumpToBottom)) return;
    // Shell-style history: ↑/↓ recall past prompts when the field is empty (or once
    // recall is underway). With text present, arrows move the caret as usual. Only the BARE
    // arrows recall — Shift+↑/↓ must stay the textarea's select-by-line (it no longer scrolls
    // the transcript, so without this guard it would fall through to recall here).
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.nativeEvent.isComposing && !e.shiftKey && !e.altKey) {
      if (e.key === "ArrowUp" && (draft === "" || histIdx !== null) && history.length) {
        e.preventDefault();
        recallPrev();
        return;
      }
      if (e.key === "ArrowDown" && histIdx !== null) {
        e.preventDefault();
        recallNext();
        return;
      }
    }
    // Don't intercept Enter while an IME candidate window is open (JP/CJK input).
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    if (modSend) {
      // Ctrl/⌘+Enter submits; plain Enter falls through to insert a newline.
      if (mod) {
        e.preventDefault();
        send();
      }
    } else if (!e.shiftKey && !mod) {
      // Enter submits; Shift+Enter falls through to insert a newline.
      e.preventDefault();
      send();
    }
  };

  // claude writes one logical response as several assistant events (text split by
  // tool calls), so merge consecutive same-role turns into one block and drop the
  // system-injected user lines (bash i/o, task notifications, slash-command echoes).
  // Append any optimistic echoes as synthetic user turns (idx past any real line so keys
  // stay unique and they sort last) — the mirror then shows a just-sent prompt at once.
  // A queued prompt that matches a pending echo upgrades that echo's badge to キュー済み
  // (no second bubble); whatever remains was typed straight into the terminal, so it gets
  // its own synthetic queued bubble. Multiset take: duplicate texts consume one entry each.
  const queuedLeft = [...queuedPrompts];
  const takeQueued = (text: string): boolean => {
    const i = queuedLeft.findIndex((q) => q.trim() === text);
    if (i < 0) return false;
    queuedLeft.splice(i, 1);
    return true;
  };
  const echoTurns: Turn[] = pendingSends
    .filter((e) => !echoLanded(e, turns, isNoise)) // hide at render the instant the real turn lands
    .map((e) => ({
      role: "user",
      text: e.text,
      idx: 1e9 + e.id,
      pending: true,
      queued: takeQueued(e.text),
    }));
  const queuedTurns: Turn[] = queuedLeft.map((q, i) => ({
    role: "user",
    text: q,
    idx: 2e9 + i,
    queued: true,
  }));
  const extras = [...queuedTurns, ...echoTurns];
  const baseTurns = coalesceUserActions(turns);
  const groups = groupTurns(extras.length ? [...baseTurns, ...extras] : baseTurns);

  // replyPending: the newest user prompt has no assistant reply after it yet — i.e. the
  // answer to the latest turn hasn't rendered. This is the signal that the mirror is still
  // waiting for the reply even if the session already reads idle.
  const replyPending = awaitingReply(groups);

  // 「ここから分岐」（docs/log/55）。条件は kind ごとに違う（canBranchInSession）— ここで絞らないと、
  // 押せるのに必ず 400 で返るボタンを出すか、逆に claude で永久に出ないかのどちらかになる。
  const canForkAt = canBranchInSession(agent.caps, { managed, readOnly });
  const openForkAt = (turn: Group) => {
    if (!canBranchFrom(turn)) return;
    setForkAtTarget({
      anchorId: turn.anchorId!,
      text: turn.text || "",
      carried: carriedUserTurns(groups, turn),
    });
  };

  // 返信サジェスト（lib/quickReplies）。直近ユーザー発話の次グループ = 最新の回答。その最終
  // テキストを B-1 ヒューリスティックの文脈に、頻度学習（settings.quickReplies）と合わせて候補化。
  const lastUserGi = latestWorkPromptIndex(groups);
  const replyGroup = lastUserGi >= 0 ? groups[lastUserGi + 1] : undefined;
  const lastReplyText = replyGroup && replyGroup.role === "assistant" ? textOfParts(replyGroup.parts) : "";
  // 「返信を頭から」の対象。レンダ中に ref へ落とすのは、[] で 1 度だけ作られる
  // ResizeObserver / onScroll のクロージャからも今の値を読ませるため（ttsCaptureRef と同型）。
  lastReplyIdxRef.current = replyGroup && replyGroup.role !== "user" ? replyGroup.idx : undefined;
  // 返信サジェスト（lib/quickReplies ＋ v2 の LLM 候補）。本体は parts/useReplySuggest。
  // 直近回答の最終テキストが候補の文脈なので、それが確定したここで呼ぶ。
  const suggest = useReplySuggest({
    session,
    settings,
    draft,
    setDraft,
    setHistIdx,
    inputRef,
    composerLocked,
    modSend,
    lastReplyText,
    send,
    toast,
    wsDown,
  });

  // Hold the "working" indicator across the idle→reply-renders gap (see `finalizing`).
  // finalizingRef is set SYNCHRONOUSLY alongside the state so the poll loop's next-tick
  // cadence sees it immediately; entering the hold also kicks a fast re-poll, because the
  // tick that first read idle already scheduled the slow (3s) interval before this ran.
  const setFinalize = (on: boolean) => {
    finalizingRef.current = on;
    setFinalizing(on);
  };
  useEffect(() => {
    if (status === "working" || bgBusy) {
      wasWorkingRef.current = true; // a turn is (or was just) running
      setFinalize(false);
      return;
    }
    if (!replyPending) {
      // The reply has landed (or there's nothing to wait for): clear, and re-arm so the
      // bridge only ever applies to a turn we actually watched run.
      wasWorkingRef.current = false;
      setFinalize(false);
      return;
    }
    if (!wasWorkingRef.current) {
      // replyPending but we never saw work this cycle (a plain history view whose last
      // turn is an interrupted, reply-less prompt) — don't invent a spinner.
      setFinalize(false);
      return;
    }
    if (!finalizingRef.current) {
      setFinalize(true);
      tickRef.current?.(); // re-poll now instead of waiting out the slow interval
    }
    // Safety valve: an interrupted turn can end with no reply at all, so never hold
    // forever — drop the indicator after a grace even if nothing lands.
    const id = setTimeout(() => {
      wasWorkingRef.current = false;
      setFinalize(false);
    }, FINALIZE_GRACE_MS);
    return () => clearTimeout(id);
    // setFinalize / refs are stable enough; re-run only on the signals that change the hold.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, bgBusy, replyPending]);

  // 新しい回答の自動読み上げ（P2）。判断は parts/useMirrorTts の syncAutoRead が持つ。ここに
  // 残すのは「いつ見直すか」＝ deps だけ（フックの中では turns / groups を購読できない）。
  useEffect(() => {
    tts.syncAutoRead({ turns, groups, status });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, status, active, readOnly, session, settings.ttsEnabled, settings.ttsAutoReadMirror, settings.ttsAutoReadAllPanes, settings.ttsWorkRead]);

  // A /context-like gauge: the newest assistant turn's prompt size (input + cache) is
  // the current context fill. The per-category split (/context) is computed inside
  // claude and isn't in the transcript, but the cache breakdown is real usage data.
  // Fallback: an agent-reported session-level fill (agy — no per-turn usage exists),
  // rendered as a single un-broken-down segment against the agent's own window.
  const ctxUsage =
    latestContext(groups) ??
    (agentCtx && agentCtx.tokens > 0
      ? { read: 0, create: 0, fresh: agentCtx.tokens, model: undefined, window: agentCtx.window }
      : null);

  // Per-assistant-turn "spend" = newly-consumed tokens (uncached input + cache creation +
  // output). Cache reads are reused context, not fresh spend, so they're excluded (the ↑
  // number still carries total context). Drives the per-turn bar and the trend Sparkline.
  const spends = groups.filter((g) => g.role !== "user").map(spendOf).filter((n) => n > 0);
  const maxSpend = spends.length ? Math.max(...spends) : 0;

  // What this reader may DO with the transcript. The mirror is the session's owner inside
  // its own Workspace, so it supplies every capability — the shared-session view supplies
  // almost none and the same blocks quietly drop those affordances (transcript/capabilities.ts).
  const caps: TranscriptCaps = {
    agentName,
    repo: sessionMeta?.repo ?? null,
    loadPastedImage: (name) =>
      raw(`api/sessions/${q(session)}/pasted/${encodeURIComponent(name)}`).then((r) => (r.ok ? r.blob() : null)),
    fileURL: downloadURL,
    openFile,
    openImage: setLightbox,
    openDiff,
    openPlan,
    session,
    sendPlanComments: (plan: string) => void sendPlanComments(plan),
    planSendDisabled: planSendBlocked,
    forkAt: canForkAt ? openForkAt : undefined,
    onReauth: () => useSettingsUI.getState().openSettings("agents"),
    tts: tts.wiring,
    expandThinking: expandThinking(settings, sessionMeta?.kind),
    isRejectedPlan: (p: string) => rejectedPlansRef.current.has(p.trim()),
    maxSpend,
    marks,
  };

  // Whether the session is in Plan mode. Case-insensitive so it holds against either the
  // labeled agent ("Plan") or an older one ("plan") — so the toggle direction (enter vs
  // exit) stays correct even before the Workspace picks up the new Agent image.
  const isPlan = mode.toLowerCase() === "plan";

  // Status chip: prefer the live polled status, fall back to the session meta.
  // rateLimitResumeAt rides along from the meta: the polled status is a bare string, so
  // without it the 制限解除待ち chip here could not say when the session moves again.
  const chip = status
    ? stateInfo({
        kind: "claude",
        alive: status !== "stopped",
        state: status,
        backgroundBusy: bgBusy,
        backgroundBusyReason: bgBusyReason,
        rateLimitResumeAt: sessionMeta?.rateLimitResumeAt,
      } as any)
    : sessionMeta
      ? stateInfo(sessionMeta)
      : null;

  // Region theme + surface color for the session mirror: data-theme scopes the base
  // tokens (tokens.css), and --chat-bg/--chat-accent are derived for the mirror's own
  // effective theme so a flipped mirror doesn't inherit the app-theme surface tint. The
  // accent falls back through the other surfaces (viewer/leftpane/topbar) as before.
  const mirrorEff = effectiveTheme(settings.mirrorTheme, settings.theme);
  const mirrorBg = surfaceBg(settings.chatColor, mirrorEff);
  const mirrorAccent =
    surfaceAccent(settings.chatColor) ||
    surfaceAccent(settings.viewerColor) ||
    surfaceAccent(settings.leftpaneColor) ||
    surfaceAccent(settings.topbarColor);

  return (
    <div
      ref={mirrorRef}
      className={"mirrorview" + (dragging ? " dragging" : "")}
      data-theme={settings.mirrorTheme !== "inherit" ? settings.mirrorTheme : undefined}
      style={{
        "--chat-font": chatFontStack(settings.chatFont),
        "--chat-size": settings.chatSize + "px",
        ...(mirrorBg ? { "--chat-bg": mirrorBg } : {}),
        ...(mirrorAccent ? { "--chat-accent": mirrorAccent } : {}),
      } as CSSProperties}
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <ViewHead
        actions={
          <>
            {/* Managed（paneless）セッションにはターミナルが無い — トグル自体を出さない。 */}
            {!managed && <MirrorToggle mirror={!!mirror} onToggle={onToggleMirror} running={running} />}
            {/* 最後＝右端。タブ付きグリッドではセル操作（別タブ／閉じる）がここに入るので、
                非タブ時に浮いているクラスタと同じ「右上の角」に見えるよう末尾に置く。 */}
            {headerActions}
          </>
        }
      >
        {sessionMeta ? (
          <PaneSessionChip session={sessionMeta} state={chip} />
        ) : (
          <span className="view-title">{tr("mirror.session_fallback")}</span>
        )}
        {managed && (
          <button
            type="button"
            className="ghost managed-settings-btn"
            disabled={!running || !alive || readOnly}
            title={running && alive && !readOnly ? tr("mirror.exec_settings_edit") : tr("mirror.exec_settings_after_resume")}
            onClick={() => setManagedSettingsOpen(true)}
          >
            <Icon name="gear" />
            {managedSettings?.model ? prettyModel(managedSettings.model) : tr("mirror.exec_settings")}
            {managedSettings?.effort && <span> · {managedSettings.effort}</span>}
            {managedSettings?.mode === "plan" && <span> · Plan</span>}
          </button>
        )}
      </ViewHead>

      {ctxUsage && <ContextBar {...ctxUsage} spends={spends} maxSpend={maxSpend} />}
      {/* 帯の key は「セッション毎に作り直す」ためのもの。★ 兄弟で同じ key を使ってはいけない —
          持ち替えで key が変わると React は残った旧 fiber を key の Map に集めて消すが、同じ key は
          後勝ちで上書きされ、前のほう（ToDo）が Map から落ちて **DOM に取り残される**。実測では
          セッションを持ち替えるたびに前のセッションの ToDo 帯が 1 枚ずつ積み上がった（dev は
          「two children with the same key」を警告するが、本番ビルドは無言）。だから接頭辞を付ける。 */}
      {tasks.length > 0 && <TaskChecklist key={"todo-" + session} tasks={tasks} session={session} />}
      <FileChangeStrip key={"files-" + session} session={session} files={files} />
      <MarkStrip key={"marks-" + session} marks={marks} storageKey={session} />
      <MirrorBanners
        isPlan={isPlan}
        termState={termState}
        compactProg={compactProg}
        suggestedTitle={suggestedTitle}
        titleActing={titleActing}
        onOpenTerminal={() => onToggleMirror(false)}
        onSkipUpdate={() => {
          postKeys(["2"]);
          setTimeout(() => tickRef.current?.(), 500);
        }}
        onAcceptTitle={acceptTitle}
        onDismissTitle={dismissTitle}
      />

      <div
        className="mirror-body"
        // 転写は「縦へ送って読む面」。折り返せない長い文字列が 1 つ混ざって横へはみ出しても、
        // それを理由にスマホの横スワイプ（セッションの持ち替え）を殺さない（app/swipeGuard.ts）。
        data-swipe-y=""
        ref={bodyRef}
        onScroll={onBodyScroll}
        onMouseUp={tts.captureSel}
        // 位置復元の打ち切り条件（endRestoreOnInput の注記）。ホイールとタッチはここで拾う —
        // .mirror-scroll の pointerdown/keydown（noteInteraction）はホイールでは出ない。
        onWheelCapture={endRestoreOnInput}
        onTouchStartCapture={endRestoreOnInput}
      >
        {/* Wrapper whose height == the transcript's total height, so a ResizeObserver can
            re-pin a bottom-stuck view to the true bottom as late content lays out — that's
            what makes opening a session land at the bottom, and keeps streaming glued to the
            tail. The jump-to-latest button stays OUTSIDE it (a direct child of the scroll
            container) so it sticks to the viewport. The interaction handlers tell that
            observer which reflows the READER caused (noteInteraction). */}
        <div
          className="mirror-scroll"
          ref={scrollBoxRef}
          onPointerDownCapture={noteInteraction}
          onKeyDownCapture={noteInteraction}
        >
        {loaded && hasMore && (
          <div className="mirror-loadmore" ref={topSentinelRef}>
            <button
              type="button"
              className="ghost mirror-loadmore-btn"
              disabled={loadingOlder}
              onClick={loadOlder}
            >
              {loadingOlder ? (
                <>
                  <Icon name="loading" spin /> {tr("chat.ph_loading")}
                </>
              ) : (
                <>
                  <Icon name="chevron-up" /> {tr("mirror.load_earlier")}
                </>
              )}
            </button>
          </div>
        )}
        {!loaded ? (
          running ? (
            // First fetch in flight (opening a session, or switching ターミナル→チャット):
            // show a spinner instead of flashing the "no conversation yet" text.
            <div className="mirror-empty muted mirror-loading">
              <Icon name="loading" spin /> {tr("chat.ph_loading")}
            </div>
          ) : (
            // Workspace stopped: the transcript can't be fetched (the Agent is down), so
            // never spin forever — say so and point at the explicit Start.
            <div className="mirror-empty muted">
              {tr("mirror.ws_stopped_history")}
            </div>
          )
        ) : groups.length === 0 && !pending && !pendingPlan && !pendingPerm && !carried && handoffs.length === 0 ? (
          // handoffs.length === 0: with an empty transcript the proposals are the only
          // thing to show, and they now live inside renderGroups (which the empty branch
          // would skip).
          <div className="mirror-empty muted">
            {readOnly
              ? tr("mirror.no_history")
              : tr("mirror.no_conversation")}
          </div>
        ) : (
          <TranscriptView
            groups={groups}
            caps={caps}
            working={busy}
            autoCollapseWork={atBottomRef.current}
            inlineCards={handoffs.map((h) => ({
              at: h.created_at,
              node: (
                <HandoffProposal
                  key={"handoff-" + h.id}
                  session={session}
                  sessionMeta={sessionMeta}
                  proposal={h}
                  onChange={(next) => updateHandoff(h.id, next)}
                />
              ),
            }))}
          />
        )}
        {carried && (
          // 持ち越し（docs/log/75）。保留カードと違い**キーは 1 つも送らない** — 当てる先の
          // モーダルはもう無く、回答は Agent が再開してから文章として届ける。
          <CarriedBlock
            carried={carried}
            session={session}
            agentName={agentName}
            onOpenPlan={openPlan}
            onError={(m) => toast(m)}
            onDone={() => setCarried(null)}
          />
        )}
        {pendingPlan && (
          <PlanPendingCard
            agentName={agentName}
            plan={pendingPlan}
            session={session}
            sending={sending}
            sendDisabled={planSendBlocked}
            onOpen={() => openPlan(pendingPlan)}
            onSendComments={() => void sendPlanComments(pendingPlan)}
            onApprove={() => {
              // A rejected plan may be refined and re-presented with identical Markdown.
              // The optimistic marker is keyed by that Markdown (the pending payload has
              // no tool-use id), so it belongs only until the next decision. Clear it
              // before approving the new presentation; its real tool_result still keeps
              // the older historical card correctly badged 却下.
              rejectedPlansRef.current.delete(pendingPlan.trim());
              void sendKeys([...PLAN_APPROVE_KEYS]);
            }}
            // 却下 = 中断（Escape）で keep-planning に倒す。ExitPlanMode メニューの
            // 選択肢数/順序は claude 版依存で、位置固定キー（旧 Down×3 で「4. Tell Claude
            // what to change」を狙う実装）は短いラップするメニューでは先頭の「Yes」行へ
            // 回り込み、却下したのに承認してしまう（2026-07-22 実障害）。中断はレイアウト
            // 非依存にモーダルを閉じて plan モードへ戻し、composer を解放する。tool_result は
            // interrupt になり planDecision.isRejected が拾う。詳細は planDecision.ts。
            onReject={() => {
              rejectedPlansRef.current.add(pendingPlan.trim()); // optimistic 却下 badge（実 outcome で planOutcome が調停）
              void sendInterrupt();
            }}
          />
        )}
        {pendingPerm && !pending && !pendingPlan && (
          // Defense-in-depth: a question/plan always wins over a generic permission
          // dialog (the server already suppresses the permission in that case). This
          // guards against a poll race ever showing 許可/拒否 over an AskUserQuestion,
          // whose buttons would send keystrokes that mis-answer the question underneath.
          <PermissionCard
            agentName={agentName}
            message={pendingPerm}
            sending={sending}
            onAllow={() => sendKeys(["Enter"])}
            onAlwaysAllow={() => sendKeys(["Down", "Enter"])}
            onDeny={() => sendKeys(["Down", "Down", "Enter"])}
          />
        )}
        {pending && pending.length > 0 && (
          <QuestionCard
            agentName={agentName}
            questions={pending}
            pendingText={pendingText}
            repo={sessionMeta?.repo ?? null}
            sending={sending}
            answerMode={sessionMeta?.kind === "claude" ? "claude" : "menu"}
            multiPage={sessionMeta?.kind === "codex"}
            writeIn={sessionMeta?.kind === "agy"}
            onOpenFile={openFile}
            onSubmitKeys={sendKeys}
            onSubmitSeq={sendSeq}
            onRespond={
              // managed は id の有無に関わらず semantic 経路に固定する — keys/seq へ
              // 落とすと存在しない tmux pane を叩きに行く。id を欠く質問（P2 まで
              // の過渡・再同期待ち）はサーバが bad_interaction で却下し、sendRespond
              // がトーストで知らせる。
              managed ? (answers) => void sendRespond(pending[0]?.id || "", answers) : undefined
            }
            onCancel={() => void sendInterrupt()}
          />
        )}
        {busy && !pending && <TypingRow agentName={agentName} sending={sending} onStop={() => void sendInterrupt()} />}
        </div>
        <JumpPills
          showJump={showJump}
          showReplyTop={showReplyTop}
          onJumpBottom={jumpToBottom}
          onJumpReplyTop={jumpToReplyTop}
        />
      </div>

      {readOnly ? (
        dirGone ? (
          <DirGoneNotice />
        ) : (
          <ResumeNotice
            running={running}
            onResume={() => {
              wantResumeFocusRef.current = true;
              onResume?.();
            }}
          />
        )
      ) : !running ? (
        // Workspace stopped (or not yet running): the agent is down, so the composer can't
        // deliver a prompt — a send would just 502. When the WS stops, the sessions poll
        // freezes and this pane's `alive` stays stuck at its last live value (a CP 502 is an
        // error, so setAlive never flips), which used to leave the live composer enabled and
        // accepting input that silently failed. Block it here and frame the mirror as the
        // read-only history it now is; Start from the top bar brings it back. (readOnly handles
        // its own stopped case above; a live agent's resume/update menu can't be up with the WS
        // down, so this precedes those checks.)
        <WsStoppedNotice />
      ) : termState === "resume" ? (
        <TerminalResumeNotice onOpenTerminal={() => onToggleMirror(false)} />
      ) : termState === "update" ? (
        <TerminalUpdateNotice
          onSkip={() => {
            postKeys(["2"]);
            setTimeout(() => tickRef.current?.(), 500);
          }}
          onSkipUntilNext={() => {
            postKeys(["3"]);
            setTimeout(() => tickRef.current?.(), 500);
          }}
        />
      ) : !alive ? (
        <ResumingNotice />
      ) : (
        <div className="mirror-compose">
          {/* 返信サジェスト: 常用短文＋直近回答に沿った候補（Layer A）＋✨で取得する LLM 候補（v2）。
              クリックで差し込み、⌥で即送信。flex 全幅 (.mirror-suggest) で入力行の上に載る。 */}
          {!composerLocked && (suggest.chips.length > 0 || settings.replySuggestEnabled) && (
            <SuggestRow
              rowRef={suggest.rowRef}
              chips={suggest.chips}
              pinned={settings.quickRepliesPinned}
              cycledText={suggest.cycledText}
              aiEnabled={!!settings.replySuggestEnabled}
              suggesting={suggest.suggesting}
              running={running}
              onFetchLlm={suggest.fetchLlmSuggestions}
              onNav={suggest.onNav}
              onChipKeyDown={suggest.onChipKeyDown}
              onChipClick={(e, text) => {
                if (suggest.chipMenu.clickSwallowed()) return; // 長タップでメニューを出した指離し
                suggest.applySuggestion(text, e.ctrlKey || e.altKey || e.metaKey);
              }}
              chipProps={suggest.chipMenu.chipProps}
            />
          )}
          {suggest.chipMenu.menu && (
            <SuggestChipMenu
              menu={suggest.chipMenu.menu}
              pinned={isQuickReplyPinned(settings.quickRepliesPinned, suggest.chipMenu.menu.text)}
              onClose={suggest.chipMenu.close}
              onTogglePin={suggest.togglePin}
              onForget={suggest.forgetSuggestion}
            />
          )}
          <AttachChips attachments={attachments} pasting={pasting} onRemove={removeAttachment} />
          <HistoryNav
            canPrev={history.length > 0}
            canNext={histIdx !== null}
            onPrev={recallPrev}
            onNext={recallNext}
          />
          {/* スキルピッカー（docs/log/50）: コンポーサー上に浮く補完リスト。マウスは onMouseMove で
              選択追従＋クリック確定（mousedown は preventDefault でフォーカスを奪わない —
              CommandPalette と同型）、タップはそのまま確定、キーボードは onKeyDown が駆動。
              引数入力中（skillArgs）は受動表示 — キーボード選択を持たないので sel も付けず、
              クリックだけ（引数は残したままコマンドを差し替える）が生きる。 */}
          {skillPicker.listVisible && (
            <SkillList
              popRef={skillPicker.popRef}
              selRef={skillPicker.selRef}
              passive={skillPicker.passive}
              skills={skillPicker.skills}
              items={skillPicker.items}
              sel={skillPicker.sel}
              query={skillPicker.query}
              onHover={skillPicker.setSel}
              onPick={skillPicker.pick}
            />
          )}
          {skillPicker.canSkills && (
            <SkillButton
              btnRef={skillPicker.btnRef}
              open={skillPicker.listVisible}
              disabled={composerLocked}
              trigger={skillPicker.trigger}
              onToggle={skillPicker.toggleFromButton}
            />
          )}
          {/* ＋ attach: the drag&drop-less path (phones foremost, handy everywhere).
              Any file type; the same addFiles upload the paste/drop paths use. */}
          {canPasteImage && (
            <>
              <input
                ref={filePickRef}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  const files = Array.from(e.target.files || []);
                  e.target.value = ""; // allow re-picking the same file
                  void addFiles(files);
                }}
              />
              <button
                type="button"
                className="ghost mirror-attach-btn"
                title={tr("mirror.attach_file")}
                disabled={composerLocked || pasting}
                onClick={() => filePickRef.current?.click()}
              >
                <Icon name="add" />
              </button>
            </>
          )}
          <textarea
            ref={inputRef}
            className="mirror-input"
            rows={2}
            placeholder={
              decisionPending
                ? pendingPlan
                  ? tr("mirror.ph_plan_wait")
                  : tr("mirror.ph_perm_wait")
                : auqLocksComposer
                  ? tr("mirror.ph_question")
                  : modSend
                    ? tr("mirror.ph_mod")
                    : tr("mirror.ph_enter")
            }
            disabled={composerLocked}
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              setHistIdx(null); // typing leaves history-recall mode
              skillPicker.trackTyping(e.target.value, e.target.selectionStart ?? e.target.value.length);
            }}
            onSelect={(e) => skillPicker.trackCaret(e.currentTarget.value, e.currentTarget.selectionStart ?? 0)}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
          />
          <SendColumn
            showMode={!!(agent.caps.planMode && agent.planCycleKey)}
            isPlan={isPlan}
            modeLabel={mode}
            modeDisabled={sending || decisionPending}
            sendDisabled={(!draft.trim() && !attachments.length) || sending || composerLocked}
            onToggleMode={() => {
              const toPlan = !isPlan;
              // Optimistic label (codex/opencode only report the new mode after a turn);
              // the poll reconciles from the terminal via paneMode.
              setMode(toPlan ? "Plan" : lastNonPlanMode.current || agent.defaultModeLabel);
              // managed のモード切替は ThreadSettings の更新（POST /settings →
              // UpdateSettings、docs/log/27 §9.4-3）— 次 turn の agent/mode に効く。
              // tui は従来どおりキー駆動（planEnterCmd / planCycleKey）。
              if (managed) {
                void sessionSettings(session, { mode: toPlan ? "plan" : "normal" });
                return;
              }
              // Low-level sends (no working status / no quick re-poll) so the optimistic
              // label holds until the regular poll reads the real mode.
              // スラッシュコマンドは turn を始めない（サーバ側 slashCmdRe が
              // working を付けない）— op は形式上 start で送る。
              if (toPlan && agent.planEnterCmd) postInput(agent.planEnterCmd, "start");
              else postKeys([agent.planCycleKey!]);
            }}
            onSend={() => send()}
          />
        </div>
      )}
      {lightbox &&
        createPortal(
          <div className="mirror-lightbox" onClick={() => setLightbox(null)} role="presentation">
            <img src={lightbox} alt={tr("mirror.pasted_image_zoom")} />
          </div>,
          document.body,
        )}
      {managedSettingsOpen && (
        <ManagedSettingsModal
          session={session}
          kind={sessionMeta?.kind || "codex"}
          working={status === "working"}
          onApplied={setManagedSettings}
          onClose={() => setManagedSettingsOpen(false)}
        />
      )}
      {forkAtTarget && (
        <ForkAtModal
          session={session}
          target={forkAtTarget}
          onDone={(name, { draft }) => {
            // 「打ち直す」分岐では、分岐点の発言を新セッションの下書きに置いてから開く。
            // 開いた瞬間に打ち直せることが要点で、元発言を探して貼り直させたら意味がない。
            // 「続きから」では発言が分岐先に残っているので draft は空で来る。
            writeDraft("af.mirror-draft." + name, draft);
            bumpSessions();
            openTargetInNew({ content: { kind: "terminal", chat: true }, session: name });
            toast(tr("mirror.fork_at_done"));
          }}
          onClose={() => setForkAtTarget(null)}
        />
      )}
      {tts.pillPortal}
    </div>
  );
}
