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
// Used by the failure block's re-auth link to open Settings > Agents (ErrorBlock).
import { useSettingsUI } from "../settings/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useDraft, writeDraft } from "../../lib/draft.ts";
import { makeAttachment, useAttachDraft } from "../../lib/attachDraft.ts";
import { autoGrowTextarea } from "../../lib/autoGrow.ts";
import { scrollComposerViewport } from "../../lib/keyScroll.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { prettyModel } from "../../lib/modelName.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { MirrorToggle } from "./MirrorToggle.tsx";
import { MirrorBanners } from "./parts/MirrorBanners.tsx";
import { useMirrorTts } from "./parts/useMirrorTts.tsx";
import { useMirrorScroll } from "./parts/useMirrorScroll.ts";
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
// workSplit lives in transcript/ alongside the turn rendering (owned by TranscriptTurn).
import { awaitingReply, latestWorkPromptIndex, textOfParts } from "./mirrorParts.ts";
import { echoLanded, echoNeedsResync } from "./pendingEcho.ts";
import { echoStore, nextEchoId, type SendEcho } from "./parts/sendEcho.ts";
import { findDiffPane, findPane, findPlanPane } from "./parts/panes.ts";
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

// MirrorView (user-facing: "chat") is a read-mostly Markdown view of a claude
// session, built on the same Agent endpoints the MCP drive tools use: GET
// /sessions/{name}/messages?since=<cursor> (the jsonl transcript as structured turns
// — role + Markdown text + timestamp — plus a line cursor and live status) and POST
// /sessions/{name}/input (tmux send-keys). It overlays the still-mounted terminal
// (Pane keeps the PTY socket alive), so the user toggles terminal/chat freely.
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
  // Managed (paneless) sessions have no terminal, so the mirror is the primary UI: no toggle
  // is rendered, and answers go out as Interaction responses (/respond) rather than keys/seq.
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
  const running = wsState === "running"; // WS down → resume is inert, mirror the terminal's resume
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
  // Compaction progress (parsed from the pane) so the "compacting" block shows a bar, not just a spinner.
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
  // follow and the work-steps fold all read it, so they can't disagree — a fold that flips
  // while the spinner is still up is exactly what shifts the text under a reader.
  const busy = status === "working" || bgBusy || finalizing;
  const [tasks, setTasks] = useState<TaskItem[]>([]); // current ToDo list (Task tool calls)
  // Files this session's agent edited (docs/log/68). Aggregated server-side over the WHOLE
  // transcript and delivered on this same poll — deriving it from `turns` would count
  // only the window the mirror happens to hold and grow as the reader scrolls up.
  const [files, setFiles] = useState<SessionFile[]>([]);
  // Prompts claude reports queued into the RUNNING turn (queue-operation events) — sent
  // mid-run from this composer or typed in the raw terminal, not yet injected. Matching
  // echoes get a "queued" badge; the rest render as synthetic queued bubbles.
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
  // Carried interaction (docs/log/75): what was on screen when the session was torn down.
  // Unlike the three pending states above there is no modal left, so the answer is delivered
  // as prose rather than keys. The server withholds `carried` while anything is pending, so
  // the two are never set at once.
  const [carried, setCarried] = useState<CarriedInteraction | null>(null);
  // Plans the user just rejected (keyed by plan text). Lets the historical plan badge read
  // "rejected" immediately, before the interrupt tool_result (its real signal) lands a poll
  // or two later — otherwise it sits at the neutral "decided" until then.
  const rejectedPlansRef = useRef<Set<string>>(new Set());
  const [mode, setMode] = useState(""); // session permission mode ("plan" | …)
  // The last non-plan mode name the terminal reported, used as the optimistic label when
  // leaving plan mode (docs/log/76).
  const lastNonPlanMode = useRef("");
  // Session-level context fill reported by the agent itself (agy /context scrape) —
  // the ContextBar's fallback when the transcript has no per-turn token usage.
  const [agentCtx, setAgentCtx] = useState<{ tokens: number; window: number } | null>(null);
  const [suggestedTitle, setSuggestedTitle] = useState(""); // headless-LLM title candidate, "" = none
  const [titleActing, setTitleActing] = useState(false); // accept/dismiss request in flight
  const [managedSettingsOpen, setManagedSettingsOpen] = useState(false);
  const [managedSettings, setManagedSettings] = useState<ManagedThreadSettings | null>(null);
  // Pending confirmation for "fork from here" (docs/log/55). null = closed.
  const [forkAtTarget, setForkAtTarget] = useState<ForkAtTarget | null>(null);
  // Composer draft, persisted per session so switching terminal/chat (which unmounts this
  // view) — or a reload — keeps what you were typing. Key by session.
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
  // against — stuck awaiting reconciliation forever. Set/read synchronously, before any state commit.
  const sendingRef = useRef(false);
  // Pasted images awaiting send: {path} is the session-saved absolute path (referenced in
  // the prompt), {url} an object URL for the local chip preview, {name} the basename.
  // Persisted per session (lib/attachDraft) like the text draft above — switching to
  // another session or to the terminal and back unmounts this view, and until the draft
  // existed that silently threw away everything staged for the turn.
  const attach = useAttachDraft(session ? "af.mirror-attach." + session : null);
  const attachments = attach.items;
  const [pasting, setPasting] = useState(false); // an attachment upload is in flight
  const [dragging, setDragging] = useState(false); // an OS file drag is hovering the pane
  const dragDepth = useRef(0); // dragenter/leave nesting counter (leave fires per child)
  const filePickRef = useRef<HTMLInputElement>(null); // the attach button's hidden picker
  const [lightbox, setLightbox] = useState<string | null>(null); // enlarged image (blob URL) or null
  // Close the enlarged-image lightbox with the device/browser Back button or a back gesture
  // (phones foremost): opening it pushes a throwaway history entry, so Back pops that instead
  // of navigating away from the Console; a tap on the backdrop consumes the entry on cleanup.
  useBackClose(lightbox ? () => setLightbox(null) : undefined, !!lightbox);
  const [histIdx, setHistIdx] = useState<number | null>(null); // position in composer history, or null
  const cursorRef = useRef(0);
  // Backward paging (P2): firstLineRef = oldest jsonl line currently held; hasMore = there
  // is older history above it to page in. loadingOlderRef guards against overlapping loads
  // (useMirrorScroll owns the height bookkeeping that keeps the viewport across a prepend).
  const firstLineRef = useRef(0);
  const [hasMore, setHasMore] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const loadingOlderRef = useRef(false);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const diagRef = useRef(""); // last transcript-diagnostic signature (warn once per change)
  const statusRef = useRef("");
  const bgBusyRef = useRef(false); // mirrors bgBusy for the poll-cadence closure (fast-poll while BG runs)
  const tickRef = useRef<(() => void) | null>(null); // lets send() trigger an immediate refresh
  const inputRef = useRef<HTMLTextAreaElement>(null);
  // All transcript scroll positioning (bottom follow, scroll-to-top of a finished turn,
  // position restore, floating pills, prepending older history) lives in parts/useMirrorScroll.
  // Called before the TTS hook because that needs bodyRef.
  const scroll = useMirrorScroll();
  const { bodyRef, atBottomRef } = scroll;

  // --- Karaoke read-aloud (turnTts, docs/log/24) -------------------------------------
  // The whole TTS set (karaoke highlighting, auto read-aloud, the quiet reading of work
  // steps, confirmation announcements, "read from here") lives in parts/useMirrorTts; only
  // the two reset call sites below remain here.
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
  // Marks drawn on the conversation (docs/log/69 / ADR 0050). This is the owner's view, so it
  // may delete anyone's mark. Do not add a poll for them — reload() rides the transcript load
  // (see the effect below).
  const marks = useMarksController({
    path: session ? `api/sessions/${q(session)}/marks` : "",
    canEdit: true,
    isOwner: true,
    viewerId: "",
    ownerLabel: tr("chat.you"),
    youLabel: tr("chat.you"),
  });
  // The transcript poll effect must not re-subscribe, so hand it the latest reload via a ref.
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
    scroll.resetPrepend();
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
    // Attachments belong to useAttachDraft: the key changes with the session, which releases
    // the old session's preview URLs and reloads the new session's draft.
    scroll.resetForSession(session); // re-take the bottom pin, restore anchor, pills, done anchor
    tts.resetForSession(); // re-baseline auto read-aloud / quiet reading / announcements (no history)
    // On leaving (switching session, switching to the terminal, closing the pane) record the
    // position being read. The session and DOM this cleanup sees belong to the OUTGOING view:
    // React runs it after the render with the new props hits the DOM but before the next
    // layout effect, and the transcript's content is state (turns), so the old session's turns
    // are still mounted and scrollTop has not moved.
    return () => {
      scroll.saveMarkFor(session);
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
        // Refreshing marks rides the transcript poll rather than adding a cycle of its own;
        // useMarksController throttles the actual round trips.
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
            tts.resetForTranscript(); // body DOM was replaced: re-baseline without stopping playback
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
          // Remember the real non-plan mode name for the optimistic label when plan mode is
          // left. Using the kind's default label instead shows "Bypass" for a claude started
          // with permission prompts on (docs/log/76); the terminal-reported value cannot make
          // that mistake.
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
          // Self-heal an unreconciled echo that can no longer land because the turn it
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

  // Re-decide bottom follow / scroll-to-top of a finished turn / position restore whenever the
  // transcript moves. The decision lives in parts/useMirrorScroll.applyFollow; only the deps
  // stay here.
  //
  // Being a LAYOUT effect is the point: it runs after the DOM changes but before paint and
  // before scroll events fire. `groups` and `loaded` move together with the deps below, so the
  // closure is fresh each time; leaving them out of the deps keeps unrelated re-renders (every
  // keystroke in the composer) from re-firing it.
  useLayoutEffect(() => {
    scroll.applyFollow({ groups, loaded, busy, pending, pendingPlan, pendingPerm });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, pending, pendingPlan, pendingPerm, status, bgBusy, finalizing, pendingSends, queuedPrompts]);



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
          scroll.capturePrependHeight(); // keep the viewport steady across the prepend
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

  // Shift the viewport back by exactly what was prepended (useMirrorScroll.applyPrependAdjust).
  useLayoutEffect(() => {
    scroll.applyPrependAdjust();
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
  // on mount. Shrinking to two rows to measure would let the transcript (this textarea's
  // sibling) grow its clientHeight in that instant, clamping a scrollTop that was pinned to
  // the bottom — keeping that shrink from escaping is autoGrowTextarea's job (see
  // lib/autoGrow.ts).
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

  // After the user hits resume-and-continue, focus the composer the moment it becomes usable
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
  // managed = the turn/start and turn/steer RPCs (P2). The result carries the rejection
  // reason so the caller can drop its optimistic echo AND tell the user why.
  // Only managed sessions pass attachments; the driver turns them into API attachments
  // (docs/log/27 §10.2-3).
  const postInput = (text: string, op: "start" | "steer", attachments?: string[]): Promise<TurnResult> =>
    sessionTurn(session, op, text, attachments);

  // wsDown: the workspace isn't running, so nothing can receive an agent-bound action —
  // it would just 502, and helpers that optimistically flip the UI to "working" would leave
  // that spinner stuck (the poll is frozen while stopped). Every send helper funnels through
  // here: the live composer is already hidden while stopped (the !running branch below), but
  // the pending permission/question/plan cards and the stop button render OUTSIDE that branch,
  // so each must self-guard. Returns true (and toasts once) when the action must be dropped.
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
  // attachments are the API attachments of a managed session (send() chooses between them and
  // weaving paths into the text). The return value says whether the session accepted the send.
  // Most callers can ignore it, but the plan-comment "sent" marker must not: returning void and
  // only toasting the failure folded away comments that never arrived, leaving them impossible
  // to retype (a comment rejected with permission_pending was immediately marked as sent).
  // restoreText is what to write back into the composer on failure. It defaults to the text
  // that was sent, but under tui that text has the attachment-path instructions woven in
  // (buildImagePrompt), so composer sends pass the text the user actually typed — the
  // attachment chips come back too, and restoring the path-bearing text would duplicate the
  // paths on the next attempt.
  const sendPrompt = async (text: string, attachments?: string[], restoreText?: string): Promise<boolean> => {
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
    // start = a new turn, steer = a follow-up into the running one. Decided from the real
    // status, before the optimistic flip to "working". Under tui both collapse to the same
    // typing, but managed's turn/start vs turn/steer (P2) depends on the distinction.
    const op = statusRef.current === "working" ? "steer" : "start";
    statusRef.current = "working";
    setStatus("working");
    // Sending is an explicit "take me to the conversation": re-arm auto-follow so the
    // optimistic echo below and the incoming reply are surfaced, even if the user had
    // scrolled up to read history.
    scroll.armFollow();
    // Show the message immediately (optimistic echo) so it never looks lost while claude
    // is busy — reconciled away once its real user turn appears in the transcript.
    const echoId = nextEchoId();
    applyEchoes((p) => [...p, { id: echoId, text: t, sinceIdx: newestIdx(), attachmentPaths: attachments, at: Date.now() }]);
    const res = await postInput(t, op, attachments);
    if (!res.ok) {
      // The send was not accepted: keeping the echo would make it look sent, so drop it,
      // toast the reason and restore the draft that send() already cleared — without
      // clobbering anything the user has started retyping.
      applyEchoes((p) => p.filter((e) => e.id !== echoId));
      toast(res.message || tr("mirror.send_failed"));
      setDraft((d) => d || restoreText || t);
    }
    sendingRef.current = false;
    setSending(false);
    // Pick up the just-logged user turn quickly rather than waiting a full interval.
    setTimeout(() => tickRef.current?.(), 250);
    return res.ok;
  };

  // Launch seed: a session started from "start work" carries a first prompt. The mirror no
  // longer SENDS it — the Agent does, from the create call's initial_prompt (or /input
  // {when_ready} when attachments made the text final only after create; useStartWork.ts).
  // That matters because this view is mounted only while its tab is the selected one:
  // typing it from here meant a session launched into a background tab sat idle until the
  // user came back to it, and then looked as if opening the tab is what sent the message.
  //
  // What is left here is display: show the sent text as an optimistic echo so the chat
  // isn't empty for the seconds between launch and the first turn reaching the transcript.
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
  // a `try/await/catch {}` here would never run its catch: a 400 (bad_key, view-nav
  // guard, rate-limit modal) left the card sitting there with no keystroke delivered and
  // no message — pressing the button appeared to do nothing. Answering is the one place where silence is
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

  // sendInterrupt stops the running turn — the equivalent of turn/interrupt, which under tui
  // becomes Escape (opencode's sub-agent detail-view special case is handled server-side in
  // /turn). The stop button only appears while working or while a background run lingers, and
  // the next poll resyncs the real state either way, so no optimistic state change is needed.
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
  // a structured answer (docs/log/27 §5). A tui question is still answered by navigating the
  // TUI modal with sendKeys/sendSeq; the server rejects /respond for tui anyway.
  const sendRespond = async (id: string, answers: InteractionAnswer[]) => {
    if (sending) return;
    if (wsDown()) return; // WS stopped: the managed session's structured answer can't be delivered
    setSending(true);
    const prev = statusRef.current;
    statusRef.current = "working";
    setStatus("working");
    const res = await sessionRespond(session, id, answers).catch((): TurnResult => ({ ok: false }));
    if (!res.ok) {
      // Never swallow a rejection (unknown id, driver not implemented, connection lost):
      // roll the status back, keep the question card alive and show the reason if there is
      // one. The next poll resyncs the real state.
      statusRef.current = prev;
      setStatus(prev);
      toast(res.message || tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
  };

  // addFiles uploads files to the session and holds each as an attachment chip —
  // shared by clipboard paste, drag&drop onto the pane, and the attach picker. Upload +
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
          // Non-images get no preview URL — the chip shows an icon + name instead.
          attach.add([makeAttachment(f, { name: nm, path })]);
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

  const removeAttachment = (i: number) => attach.remove(i);
  // Discard the draft only once the send succeeded, i.e. the paths made it into the prompt.
  const clearAttachments = () => attach.clear();

  // An AskUserQuestion can't be answered by the composer's free text — verified against
  // the terminal (v2.1.204, docs/build/92): the modal IGNORES typed text on option rows
  // entirely (the older "option filter" behavior is gone), so the trailing Enter just
  // confirms the highlighted (first) option — a silent wrong answer. Digit keys 1-9 even
  // select-and-submit instantly, so stray text is doubly dangerous. Lock the composer for
  // ANY pending question and steer the user to the card — its options key-drive the modal
  // (Down×i, Enter) and its free-text row uses the still-working "Type something" path.
  // An empty array (no questions) must not lock: the card only renders for pending.length > 0,
  // so `!!pending` alone would kill the composer with no card to answer in.
  const auqLocksComposer = !!pending?.length;
  // A pending plan approval or permission prompt is a menu decision, NOT a free-text turn:
  // sending would type text + Enter, and that Enter selects the menu's default (approve /
  // allow), silently confirming it. A mode toggle would likewise mis-key the menu. So lock
  // the composer AND the mode chip while one is pending; act via the card's buttons.
  const decisionPending = !!pendingPlan || !!pendingPerm;
  const composerLocked = auqLocksComposer || decisionPending;

  // OS drag&drop anywhere on the pane attaches the dropped files (the composer is a
  // small target — the whole chat area accepts). dragenter/leave nest per child, so a
  // depth counter drives the highlight; drop is ignored while the composer is hidden
  // (read-only history) or locked.
  const canDropFiles = canPasteImage && !readOnly && !composerLocked;
  // A memo dragged from the left-pane queue drops its text into the composer — but ONLY
  // when this session is awaiting input (alive and idle: not working, no lingering background run,
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

  // With an override, send that text (a suggestion chip's Alt-click instant send); otherwise
  // send the composer's draft.
  const send = async (override?: string) => {
    if (composerLocked) return;
    const text = (override ?? draft).trim();
    if (!text && !attachments.length) return;
    // Short plain text feeds the reply-suggestion learning. Only sends through here count, so
    // AUQ/plan answers are naturally excluded. Re-sending a phrase that was hidden from the
    // menu says the user wants it back, so unhide it.
    if (text && isQuickReplyCandidate(text, attachments.length > 0)) {
      setSetting("quickReplies", recordQuickReply(settings.quickReplies || {}, text, Date.now()));
      const hidden = settings.quickRepliesHidden || [];
      const unhidden = unhideQuickReply(hidden, text);
      if (unhidden !== hidden) setSetting("quickRepliesHidden", unhidden);
    }
    // Sending from the composer while this session is being read aloud stops that playback:
    // the user is interrupting or following up, so hearing the old answer out is only
    // confusing. Matched by sessionName, so playback from another session keeps running.
    const ts = useTtsStore.getState();
    if (ts.active && ts.sessionName === session) ts.stop();
    const staged = attachments; // restored on failure (revive, below)
    const paths = attachments.map((a) => a.path);
    // managed passes them as wire attachments (the driver converts them into API attachments,
    // docs/log/27 §10.2-3); tui weaves the paths into the prompt body.
    const prompt = managed ? text : buildImagePrompt(text, paths, agent.id);
    setHistIdx(null);
    setDraft("");
    clearAttachments();
    // On touch devices, drop focus so the soft keyboard (GBoard) retracts once the
    // turn is sent — the reply is what the user wants to read, not keep typing. Desktop
    // keeps focus (and refocuses below) so typing the next turn needs no extra click.
    if (coarsePointer()) inputRef.current?.blur();
    // Restore the attachments too when the send is refused. Restoring only the text is the
    // worst outcome: the message is back, so the user re-sends believing it is the same turn,
    // and sends one with no images.
    if (!(await sendPrompt(prompt, managed ? paths : undefined, text))) attach.revive(staged);
    if (!coarsePointer()) inputRef.current?.focus();
  };


  // --- Skill picker (docs/log/50) --- implemented in parts/useSkillPicker; called here
  // because it reads composerLocked.
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

  // Why plan comments cannot be sent ("" = they can). The composer disappears entirely while
  // stopped, but the plan card stays in the history, so its send button must block itself.
  // Collecting comments while stopped is still allowed — they go out after a resume.
  const planSendBlocked = !running
    ? tr("mirror.ws_stopped")
    : !alive || readOnly
      ? tr("plan.send_needs_running")
      : "";

  // When reject → revise → re-present replaces the plan text, follow it in the open review
  // pane too. Without this the reader comments against the old text and only discovers after
  // sending that the passage is gone — a doc pane is a snapshot and stays stale silently.
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

  // Deliver comments on a plan. Which route to send by, and when to mark them sent, is
  // decided by deliverPlanComments (planComments.ts): this component is too large to have a
  // rendering test, so the decision is lifted out and pinned by unit tests. What stays here
  // is the React housekeeping — press guard, optimistic "rejected" badge, toast, echo.
  const sendPlanComments = async (plan: string) => {
    if (sending) return;
    // Nothing reaches a stopped session. The plan card also renders in the history, so it
    // does not get the composer's "hidden while stopped" protection and must refuse here.
    // The button is disabled via planSendBlocked too — this is the second layer, for a press
    // that races the stop.
    if (planSendBlocked) {
      toast(planSendBlocked);
      return;
    }
    if (wsDown()) return;
    const isPending = !!pendingPlan && pendingPlan.trim() === plan.trim();
    if (isPending) {
      // The route that carries a rejection: block the send button and clear the badge and
      // awaiting-reply state up front. (sendPrompt manages `sending` itself, so the
      // speak-only route leaves it alone.)
      setSending(true);
      rejectedPlansRef.current.add(plan.trim()); // optimistic "rejected" badge; planOutcome reconciles it
      wasWorkingRef.current = false; // as with an interrupt: no reply is being waited for
      finalizingRef.current = false;
      setFinalizing(false);
    }
    const res = await deliverPlanComments(planKey(session, plan), {
      pending: isPending,
      respond: (feedback) => sessionPlanRespond(session, "reject", feedback),
      say: (feedback) => sendPrompt(feedback),
    });
    if (isPending) setSending(false);
    if (!res) return; // nothing to send
    if (!res.ok) {
      // Undelivered means the comments were not folded away, so state the reason and make it
      // clear they can be re-sent (undelivered = the rejection went through but the text did
      // not). A failure on the say route is not re-toasted: sendPrompt has already given the
      // concrete reason (awaiting permission, stopped, …) and a generic "send failed" on top
      // of it would obscure what happened.
      if (res.reason !== "say") {
        toast(res.message || tr(res.reason === "undelivered" ? "plan.feedback_undelivered" : "mirror.send_failed"));
      }
      return;
    }
    if (res.via === "reject") {
      const echoId = nextEchoId(); // optimistic echo until the real turn lands (as in sendPrompt)
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
  // "changes in this session" mode), so they don't have to poll the transcript themselves.
  useEffect(() => {
    if (session) useSessionFilesStore.getState().set(session, files);
  }, [session, files]);

  // Auto-suggested title (session_title.go): accepting promotes it to the session's real
  // title (bumpSessions so the left-pane label updates without waiting for its own
  // poll); dismissing discards it. Either way the server never offers one again.
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
    if (!e.nativeEvent.isComposing && scrollComposerViewport(e, bodyRef.current, scroll.jumpToBottom)) return;
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
  // A queued prompt that matches a pending echo upgrades that echo's badge to "queued"
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

  // "Fork from here" (docs/log/55). The conditions differ per kind (canBranchInSession);
  // without filtering here the button would either be pressable but always 400, or never
  // appear at all for claude.
  const canForkAt = canBranchInSession(agent.caps, { managed, readOnly });
  const openForkAt = (turn: Group) => {
    if (!canBranchFrom(turn)) return;
    setForkAtTarget({
      anchorId: turn.anchorId!,
      text: turn.text || "",
      carried: carriedUserTurns(groups, turn),
    });
  };

  // Reply suggestions (lib/quickReplies). The group after the newest user message is the
  // latest reply; its final text is the context for the B-1 heuristic, combined with the
  // frequency learning in settings.quickReplies.
  const lastUserGi = latestWorkPromptIndex(groups);
  const replyGroup = lastUserGi >= 0 ? groups[lastUserGi + 1] : undefined;
  const lastReplyText = replyGroup && replyGroup.role === "assistant" ? textOfParts(replyGroup.parts) : "";
  // Target of "reply from the top". Written to a ref during render so the ResizeObserver /
  // onScroll closures — created once under [] — can read the current value (same shape as
  // ttsCaptureRef).
  scroll.lastReplyIdxRef.current = replyGroup && replyGroup.role !== "user" ? replyGroup.idx : undefined;
  // Reply suggestions (lib/quickReplies plus v2's LLM candidates), implemented in
  // parts/useReplySuggest. Called here because the latest reply's final text is the context
  // for the candidates and is only settled at this point.
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

  // Auto read-aloud of a new reply (P2). The decision lives in parts/useMirrorTts.syncAutoRead;
  // all that stays here is when to re-evaluate it — the deps, which the hook cannot subscribe
  // to (turns / groups are not visible inside it).
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
  // without it the rate-limit-wait chip here could not say when the session moves again.
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
      ref={scroll.mirrorRef}
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
            {/* A managed (paneless) session has no terminal, so the toggle is not rendered. */}
            {!managed && <MirrorToggle mirror={!!mirror} onToggle={onToggleMirror} running={running} />}
            {/* Last = rightmost. In the tabbed grid the cell actions (pop out / close) go
                here, so keep them at the end to occupy the same top-right corner as the
                floating cluster does outside tabbed mode. */}
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
      {/* These keys exist to rebuild each strip per session, and siblings must never share one.
          When the key changes, React collects the leftover fibers in a Map keyed by key; a
          duplicate is overwritten last-wins, so the earlier one (ToDo) falls out of the Map and
          is left stranded in the DOM. Measured: every session switch stacked up one more of the
          previous session's ToDo strips (dev warns "two children with the same key", a
          production build is silent). Hence the prefixes. */}
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
        // The transcript is a vertically scrolled reading surface: one unwrappable long string
        // overflowing horizontally must not kill the phone's horizontal swipe between sessions
        // (app/swipeGuard.ts).
        data-swipe-y=""
        ref={bodyRef}
        onScroll={scroll.onBodyScroll}
        onMouseUp={tts.captureSel}
        // How position restore is abandoned (see the note on endRestoreOnInput). Wheel and touch
        // are caught here: .mirror-scroll's pointerdown/keydown (noteInteraction) never fire for
        // a wheel.
        onWheelCapture={scroll.endRestoreOnInput}
        onTouchStartCapture={scroll.endRestoreOnInput}
      >
        {/* Wrapper whose height == the transcript's total height, so a ResizeObserver can
            re-pin a bottom-stuck view to the true bottom as late content lays out — that's
            what makes opening a session land at the bottom, and keeps streaming glued to the
            tail. The jump-to-latest button stays OUTSIDE it (a direct child of the scroll
            container) so it sticks to the viewport. The interaction handlers tell that
            observer which reflows the READER caused (noteInteraction). */}
        <div
          className="mirror-scroll"
          ref={scroll.scrollBoxRef}
          onPointerDownCapture={scroll.noteInteraction}
          onKeyDownCapture={scroll.noteInteraction}
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
            // First fetch in flight (opening a session, or switching terminal → chat):
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
          // Carried interaction (docs/log/75). Unlike the pending card this sends no keys at
          // all: there is no modal left to aim at, and the Agent delivers the answer as prose
          // after resuming.
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
              // the older historical card correctly badged as rejected.
              rejectedPlansRef.current.delete(pendingPlan.trim());
              void sendKeys([...PLAN_APPROVE_KEYS]);
            }}
            // Reject = interrupt (Escape), which falls back to keep-planning. The number and
            // order of the ExitPlanMode menu's options depend on the claude version, so a
            // position-fixed key sequence (aiming at "4. Tell Claude what to change" with
            // Down×3) wraps around to the leading "Yes" row on a shorter menu and approves the
            // plan the user meant to reject — a real incident. An interrupt closes the modal
            // independently of layout, returns to plan mode and releases the composer; the
            // tool_result becomes an interrupt, which planDecision.isRejected picks up. See
            // planDecision.ts.
            onReject={() => {
              rejectedPlansRef.current.add(pendingPlan.trim()); // optimistic "rejected" badge; planOutcome reconciles it
              void sendInterrupt();
            }}
          />
        )}
        {pendingPerm && !pending && !pendingPlan && (
          // Defense-in-depth: a question/plan always wins over a generic permission
          // dialog (the server already suppresses the permission in that case). This
          // guards against a poll race ever showing allow/deny over an AskUserQuestion,
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
              // A managed session is pinned to the semantic route whether or not an id is
              // present: falling back to keys/seq would drive a tmux pane that does not
              // exist. A question missing its id (a transitional or resyncing case up to P2)
              // is rejected server-side with bad_interaction, and sendRespond toasts that.
              managed ? (answers) => void sendRespond(pending[0]?.id || "", answers) : undefined
            }
            onCancel={() => void sendInterrupt()}
          />
        )}
        {busy && !pending && <TypingRow agentName={agentName} sending={sending} onStop={() => void sendInterrupt()} />}
        </div>
        <JumpPills
          showJump={scroll.showJump}
          showReplyTop={scroll.showReplyTop}
          onJumpBottom={scroll.jumpToBottom}
          onJumpReplyTop={scroll.jumpToReplyTop}
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
              // For a managed session the mode switch is a ThreadSettings update (POST
              // /settings → UpdateSettings, docs/log/27 §9.4-3), which takes effect on the
              // next turn's agent/mode. tui stays key-driven (planEnterCmd / planCycleKey).
              if (managed) {
                void sessionSettings(session, { mode: toPlan ? "plan" : "normal" });
                return;
              }
              // Low-level sends (no working status / no quick re-poll) so the optimistic
              // label holds until the regular poll reads the real mode.
              // A slash command starts no turn (the server's slashCmdRe keeps it out of
              // "working"), so the op is sent as start purely as a formality.
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
            // In redo mode, seed the new session's draft with the fork point's message before
            // opening it: the point is being able to retype straight away, which is lost if the
            // user has to hunt down the original and paste it back. In continue mode the message
            // is still in the forked conversation, so the draft arrives empty.
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
