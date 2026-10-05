import { useEffect, useMemo, useRef, useState } from "react";
import type {
  CarriedInteraction,
  Discard,
  ManagedThreadSettings,
  QueueItem,
  PendingPeer,
} from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { streamReplies, type Settings } from "../../../lib/settings.ts";
import { useDraft } from "../../../lib/draft.ts";
import { useAttachDraft } from "../../../lib/attachDraft.ts";
import { useBackClose } from "../../../lib/backClose.ts";
import { useHandoffProposals, type Proposal as HandoffProposalT } from "../HandoffProposal.tsx";
import type { ForkAtTarget } from "../ForkAtModal.tsx";
import { emptyDiscardNotices, queueEntries, visibleDiscards, type DiscardNoticeState } from "../stopQueue.ts";
import type { SessionFile } from "../sessionFiles.ts";
import type { PendingApproval, Question, TaskItem, Turn } from "../transcript/types.ts";
import { echoStore, type SendEcho } from "./sendEcho.ts";

/**
 * useMirrorState declares MirrorView's per-session view state: what the transcript poll fills
 * in, the composer's draft and attachments, and the refs the poll loop reads across renders.
 * useTranscriptPoll resets and fills it; MirrorView and its other parts read it.
 */
export function useMirrorState({
  session,
  sessionMeta,
  settings,
}: {
  session: string;
  sessionMeta?: Session | null;
  settings: Settings;
}) {
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
  // The same queue with ids, origins and states (ADR 0105), sent only by a Managed session on
  // an Agent that has it. null = not sent: the bubbles then come from queuedPrompts and carry
  // no actions.
  const [queuedItems, setQueuedItems] = useState<QueueItem[] | null>(null);
  // What second stops threw away and the driver still keeps (decision 4), and this tab's own
  // progress through them — see stopQueue.ts for why a restored discard is held locally.
  const [discards, setDiscards] = useState<Discard[]>([]);
  const [discardNotices, setDiscardNotices] = useState<DiscardNoticeState>(emptyDiscardNotices);
  // Peer messages the Agent holds until the member answers the pending prompt (#1031).
  const [pendingPeers, setPendingPeers] = useState<PendingPeer[]>([]);
  // Queue entries with a remove request in flight, so a double click sends one request.
  const queueOpsRef = useRef<Set<string>>(new Set());
  const queueShown = useMemo(() => queueEntries(queuedItems, queuedPrompts), [queuedItems, queuedPrompts]);
  const queuedCount = queueShown.length;
  const discardView = useMemo(() => visibleDiscards(discards, discardNotices), [discards, discardNotices]);
  const [alive, setAlive] = useState(!!sessionMeta?.alive); // live session ⇒ composer usable
  // The working dir was removed (repo/worktree deleted): the transcript survives
  // (stored under the agent's home), so history stays readable, but resume is
  // impossible — BuildLaunch refuses a gone dir. Offer a note, not a resume button.
  const dirGone = sessionMeta?.resumable === false && !alive;
  const [pending, setPending] = useState<Question[] | null>(null); // currently-awaiting AskUserQuestion
  const [pendingText, setPendingText] = useState<string>(""); // prose streamed just before the pending question
  // The reply claude is still writing (#1250), sent by the Agent only while a turn runs and only
  // when this poll asked for it (?live=1). Only claude's route streams it; the per-kind setting
  // turns it off. Line by line and typewriter (#1274) differ only in how LiveReplyCard shows it:
  // the request is the same. Read through a ref inside the poll loop, which outlives renders.
  const liveMode = sessionMeta?.kind === "claude" ? streamReplies(settings, sessionMeta?.kind) : "off";
  const liveOn = liveMode !== "off";
  const liveOnRef = useRef(liveOn);
  liveOnRef.current = liveOn;
  const [liveText, setLiveText] = useState("");
  useEffect(() => {
    if (!liveOn) setLiveText(""); // switched off: drop what is shown now, not at the next poll
  }, [liveOn]);
  const [pendingPlan, setPendingPlan] = useState<string | null>(null); // ExitPlanMode plan awaiting approval
  const [pendingPerm, setPendingPerm] = useState<string | null>(null); // tool-permission prompt awaiting allow/deny
  // A MANAGED session's tool approval. Kept apart from pendingPerm because the two are
  // answered by different mechanisms — keystrokes into a pane versus /respond by id — and a
  // managed session has no pane for the first one.
  const [pendingApproval, setPendingApproval] = useState<PendingApproval | null>(null);
  // Carried interaction (docs/log/75): what was on screen when the session was torn down.
  // Unlike the three pending states above there is no modal left, so the answer is delivered
  // as prose rather than keys. The server withholds `carried` while anything is pending, so
  // the two are never set at once.
  const [carried, setCarried] = useState<CarriedInteraction | null>(null);
  // Plans the user just rejected (keyed by plan text). Lets the historical plan badge read
  // "rejected" immediately, before the interrupt tool_result (its real signal) lands a poll
  // or two later — otherwise it sits at the neutral "decided" until then.
  const rejectedPlansRef = useRef<Set<string>>(new Set());
  // Bumped whenever that set is written. The badge is READ while a turn renders, and the turns are
  // memoized, so a silent mutation would not reach the card until the next transcript change —
  // which is exactly the poll or two this optimism exists to cover. markRejected is the only
  // writer, the session reset aside — that one changes `session`, which rebuilds caps anyway.
  const [rejectedGen, setRejectedGen] = useState(0);
  const markRejected = (plan: string, rejected: boolean) => {
    if (rejected) rejectedPlansRef.current.add(plan.trim());
    else rejectedPlansRef.current.delete(plan.trim());
    setRejectedGen((n) => n + 1);
  };
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
  // The enlarged image: its URL (a blob for a pasted image, the download URL for a shared
  // file) plus, when the image is a file, the path it came from — that is what lets the
  // lightbox bar offer its folder's gallery (ADR 0080 decision 7).
  const [lightbox, setLightbox] = useState<{ src: string; path?: string } | null>(null);
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
  // Last transcript payload, verbatim, and how many polls in a row have returned exactly it.
  // Together they are the "nothing moved" signal: it suppresses a re-render that would change
  // nothing (see the poll) and it drives the cadence ladder (pollCadence.ts).
  const lastPayloadRef = useRef("");
  const unchangedRef = useRef(0);
  const lastPollAtRef = useRef(0);
  // Digest of the whole-transcript aggregates (files / tasks / answers) we already hold. Sent
  // back on each steady-state poll so the Agent can leave them out of the response instead of
  // rebuilding and re-sending them every tick (session_transcript_agg.go).
  const aggSigRef = useRef("");
  const tickRef = useRef<(() => void) | null>(null); // lets send() trigger an immediate refresh
  const inputRef = useRef<HTMLTextAreaElement>(null);
  return {
    turns, setTurns, stateSession, setStateSession, pendingSends, setPendingSends, pendingSendsRef,
    applyEchoes, loaded, setLoaded, handoffs, setHandoffs, updateHandoff, termState, setTermState,
    compactProg, setCompactProg, status, setStatus, bgBusy, setBgBusy, bgBusyReason, setBgBusyReason,
    finalizing, setFinalizing, finalizingRef, wasWorkingRef, busy, tasks, setTasks, files, setFiles,
    queuedPrompts, setQueuedPrompts, queuedItems, setQueuedItems, discards, setDiscards, discardNotices,
    setDiscardNotices, pendingPeers, setPendingPeers, queueOpsRef, queueShown, queuedCount, discardView,
    alive, setAlive, dirGone, pending, setPending, pendingText, setPendingText, liveMode, liveOn, liveOnRef,
    liveText, setLiveText, pendingPlan, setPendingPlan, pendingPerm, setPendingPerm, pendingApproval,
    setPendingApproval, carried, setCarried, rejectedPlansRef, rejectedGen, setRejectedGen, markRejected,
    mode, setMode, lastNonPlanMode, agentCtx, setAgentCtx, suggestedTitle, setSuggestedTitle, titleActing,
    setTitleActing, managedSettingsOpen, setManagedSettingsOpen, managedSettings, setManagedSettings,
    forkAtTarget, setForkAtTarget, draftKey, draft, setDraft, sending, setSending, sendingRef, attach,
    attachments, pasting, setPasting, dragging, setDragging, dragDepth, filePickRef, lightbox, setLightbox,
    histIdx, setHistIdx, cursorRef, firstLineRef, hasMore, setHasMore, loadingOlder, setLoadingOlder,
    loadingOlderRef, topSentinelRef, diagRef, statusRef, bgBusyRef, lastPayloadRef, unchangedRef,
    lastPollAtRef, aggSigRef, tickRef, inputRef,
  };
}

export type MirrorState = ReturnType<typeof useMirrorState>;
