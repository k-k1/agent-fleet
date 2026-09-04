import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { chatFontStack, effectiveTheme, expandThinking, surfaceAccent, surfaceBg, useSettings } from "../../lib/settings.ts";
import { HandoffBody, HandoffCard, type Proposal } from "../mirror/HandoffProposal.tsx";
import { applyMark, captureMark, loadMark, saveMark, type ScrollMark } from "../mirror/scrollMark.ts";
import { TranscriptView } from "../mirror/transcript/TranscriptView.tsx";
import { PlanBlock, QuestionBlock } from "../mirror/transcript/blocks.tsx";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import type { TranscriptCaps } from "../mirror/transcript/capabilities.ts";
import type { Question, Turn } from "../mirror/transcript/types.ts";
import { coalesceUserActions, groupTurns, mergeTurns } from "../mirror/transcript/model.ts";
import { patchAnswers } from "../mirror/interactionAnswers.ts";
import { ownerLabel, useSharedSessionsStore } from "./store.ts";
import { HandoffInboxModal } from "./HandoffInboxModal.tsx";
import { startHandoffPolling, useHandoffStore } from "./handoffStore.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useMarksController } from "../mirror/transcript/useMarks.ts";
import { MarkStrip } from "../mirror/transcript/MarkStrip.tsx";
import "./sharing.css";

// SharedSessionView — the RECIPIENT's read of a session somebody else owns (docs/log/59).
//
// It renders through the very same pipeline and blocks as the mirror
// (features/mirror/transcript), so a shared conversation reads exactly like the owner's:
// tool runs folded, plans and reasoning as their own cards, compaction summaries
// collapsed. What differs is only the TranscriptCaps handed in — a recipient has no
// Workspace of their own to open files, diffs, panes or pasted images in, so those
// affordances are simply not rendered (see transcript/capabilities.ts).
//
// The transcript arrives through the control-plane's allowlist DTO, which strips cwd /
// path / filePath and every structured coordinate before it ever reaches the browser
// (docs/log/59 §3, control-plane/session_share.go sharedTranscriptDTO).

// Page size, in transcript LINES (claude) / turns (store-backed agents) — the same
// window the mirror asks for. It used to be 60 for a faster first paint, but 60 claude
// jsonl lines is often a fraction of ONE exchange (a single answer spans a thinking
// line, every tool call and the reply), so the opening screen could start mid-answer
// with the prompt that caused it out of frame, and the "load earlier conversation"
// button had to be pressed over and over. The first-paint cost that motivated 60 was
// the per-request inventory sync, which is now throttled per owner (docs/log/59 §3).
const WINDOW = 400;
// Poll cadence, matching the mirror's. The server allows 120 reads/min per
// recipient+session, so even the working cadence stays well inside the limit.
const POLL_WORKING = 1200;
const POLL_IDLE = 3000;
// The owner's Workspace is stopped: nothing can change until they start it, so back off.
const POLL_STOPPED = 5000;
// Fetch interval for handoff proposals. Coarser than the transcript is fine (a proposal only
// changes when the owner acts), and it shares the transcript's 120 reads/min bucket, so
// tightening it here throttles the transcript instead.
const POLL_HANDOFF = 5000;
const NEAR_BOTTOM_PX = 80;
// Window in which height changes caused by the reader's own expand/collapse are not followed.
// Same value as the mirror.
const INTERACT_HOLD_MS = 600;
// How long scrolling must be quiet before the reading position is recorded.
const MARK_SETTLE_MS = 150;

interface SharedTurn extends Turn {
  status?: string;
}

// Last-known transcript per shared session, kept at module level so re-opening a pane
// paints immediately instead of starting from an empty view while the first fetch flies.
// Same reasoning as the mirror's echoStore: this component unmounts on a pane/tab switch,
// and re-fetching from scratch every time is exactly what made the view feel slow.
interface CacheEntry {
  turns: SharedTurn[];
  cursor: number;
  firstLine: number;
  hasMore: boolean;
}
const transcriptCache = new Map<string, CacheEntry>();

export function SharedSessionView({ sharedSessionId, headerActions }: { sharedSessionId: string; headerActions?: ReactNode }) {
  const tr = useT();
  const settings = useSettings();
  const meta = useSharedSessionsStore((s) => s.sessions.find((x) => x.id === sharedSessionId));
  // My own login id, used to decide which marks read as "you" and which ones I may delete.
  const myEmail = useTenantStore((s) => s.whoami?.email || "");
  const refreshList = useSharedSessionsStore((s) => s.refresh);
  const cached = transcriptCache.get(sharedSessionId);
  const [turns, setTurns] = useState<SharedTurn[]>(cached?.turns ?? []);
  const [loaded, setLoaded] = useState(!!cached);
  const [hasMore, setHasMore] = useState(cached?.hasMore ?? false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  // Show the jump-to-latest pill only while reading back up, i.e. while auto-follow is off. Same
  // look and wording as the mirror; without it the recipient cannot tell that something arrived.
  const [showJump, setShowJump] = useState(false);
  // Handoffs proposed by the owner (propose_session_handoff). The transcript keeps only the tool
  // line and a boilerplate sentence, while the body lives in a separate store on the owner side,
  // so without this the recipient sees that a handoff happened but not what was handed over.
  const [handoffs, setHandoffs] = useState<Proposal[]>([]);
  // Handoffs another member sent to me (docs/log/77). Notifications land on this surface, so with
  // no way to accept here the person who clicked the notification reaches a dead end (the only
  // other entry point was an icon on the rail heading). The selectors must return only the id and
  // name: returning the offer itself yields a new object on every 15-second refetch and repaints
  // the whole surface the reader is on.
  const offerId = useHandoffStore((st) => st.received.find((o) => o.sessionId === sharedSessionId)?.id ?? "");
  const offerFrom = useHandoffStore((st) => st.received.find((o) => o.sessionId === sharedSessionId)?.ownerUserKey ?? "");
  const [offerOpen, setOfferOpen] = useState(false);
  // This surface subscribes to the backlog itself so the band also appears in a detached tab with
  // no rail. The polling is refcounted down to a single timer.
  useEffect(() => startHandoffPolling(), []);
  // The modal currently open (AskUserQuestion / ExitPlanMode). Like the owner-facing answer it
  // arrives outside the transcript: while one is open the Agent removes that question/plan from
  // messages and holds the cursor before it (hidePendingInteraction), so without rendering it here
  // the recipient would see nothing at all for as long as the question is up. This surface is
  // read-only, so no way to answer is offered (never render a control that cannot be pressed —
  // transcript/capabilities.ts).
  const [pendingQuestions, setPendingQuestions] = useState<Question[] | null>(null);
  const [pendingText, setPendingText] = useState("");
  const [pendingPlan, setPendingPlan] = useState<string | null>(null);
  const seen = useRef(""); // the last proposals received; identical content leaves state untouched
  const cursor = useRef(cached?.cursor ?? 0);
  const firstLine = useRef(cached?.firstLine ?? 0);
  const bodyRef = useRef<HTMLDivElement>(null);
  // Inner wrapper whose height equals the content. The ResizeObserver watches this too, because
  // the scroll container itself has the pane's dimensions and never fires as the transcript grows.
  // Same role as the mirror's .mirror-scroll.
  const scrollBoxRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  // The last scrollTop we wrote ourselves. onScroll compares against it to tell "content grew
  // below my pin" from "the reader moved up" (same as the mirror's selfTopRef): judging by raw
  // distance reads the growth right after a self-pin as the reader scrolling up and drops follow.
  const selfTop = useRef(0);
  // Height changes up to this instant count as the reader expanding something themselves (work
  // trace and the like) and are not followed.
  const interactUntil = useRef(0);
  // Position restore (scrollMark): where to land when coming back, and whether a restore is on.
  const restoreMark = useRef<ScrollMark | null>(null);
  const restoring = useRef(false);
  // Whether the first landing for this session (tail or restore) has happened.
  const didInit = useRef(false);
  // The position being read, recorded whenever scrolling settles. It cannot be measured again on
  // the way out (see the cleanup note below), so record it while the DOM is still alive.
  const pendingMark = useRef<ScrollMark | null>(null);
  const markTimer = useRef(0);
  const loadingOlderRef = useRef(false);
  // Set while prepending older history, to keep the reader's position put (below).
  const anchor = useRef<number | null>(null);

  const path = `api/shared-sessions/${encodeURIComponent(sharedSessionId)}`;
  // Reading positions live in the same module-level Map as the mirror's. The owner side keys by
  // session name and this one by catalog id, so a prefix keeps the two from mixing.
  const markKey = `shared:${sharedSessionId}`;
  // Marks drawn on the conversation (docs/log/69 / ADR 0050). RO may read them, only RW may draw.
  // You may delete only your own (the Agent decides; the CP stamps the login id). While the
  // owner's Workspace is stopped they are not fetched, same as the transcript.
  const marks = useMarksController({
    path: `${path}/marks`,
    canEdit: meta?.permission === "rw",
    isOwner: false,
    viewerId: myEmail,
    ownerLabel: meta ? ownerLabel(meta) : "",
    youLabel: tr("chat.you"),
    paused: !!meta && meta.workspaceState !== "running",
  });
  // Called from the handoff-proposal polling effect so no new interval is created; the actual
  // round trips are throttled inside useMarksController.
  const marksReloadRef = useRef(marks.reload);
  marksReloadRef.current = marks.reload;

  useEffect(() => {
    const entry = transcriptCache.get(sharedSessionId);
    setTurns(entry?.turns ?? []);
    setLoaded(!!entry);
    setHasMore(entry?.hasMore ?? false);
    setError("");
    // Pending interactions are not cached, so another session's modal cannot flash here; the
    // first poll fills them in.
    setPendingQuestions(null);
    setPendingText("");
    setPendingPlan(null);
    cursor.current = entry?.cursor ?? 0;
    firstLine.current = entry?.firstLine ?? 0;
    atBottom.current = true;
    setShowJump(false); // right after opening another session we are at the tail
    // Kick a list refresh so the header meta fills in if the store is still cold — but
    // never await it. Blocking the first transcript fetch behind a full
    // GET /api/shared-sessions (which probes every owner's Workspace state in turn) was
    // the single biggest reason a shared session took so long to show anything.
    void refreshList();

    let live = true;
    let timer = 0;
    const tick = async () => {
      if (!live) return;
      // Read the owner's Workspace state from the store rather than fetching it: the
      // global 5s poll (store.ts startSharedSessionsPolling) already keeps it current.
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (current && current.workspaceState !== "running") {
        setError(tr("share.owner_stopped"));
        timer = window.setTimeout(tick, POLL_STOPPED);
        return;
      }
      // A missing entry is NOT treated as "no access": the store may simply not have
      // loaded yet, and the server is the authority — it answers 404 if the share is gone.
      const first = cursor.current === 0;
      const url = first ? `${path}/messages?since=0&tail=1&limit=${WINDOW}` : `${path}/messages?since=${cursor.current}`;
      const d = await api(url).catch(() => ({ error: { message: tr("share.load_failed") } }));
      if (!live) return;
      if (d?.error) {
        setError(errText(d.error));
      } else {
        setError("");
        setLoaded(true);
        if (typeof d.cursor === "number") cursor.current = d.cursor;
        if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
        if (typeof d.hasMore === "boolean") setHasMore(d.hasMore);
        setWorking(d.status === "working");
        // The server returns the current pending state on every poll, independent of the window
        // even for incremental fetches, so "absent" means "no longer open". Keeping the previous
        // value would leave a settled modal on screen for the recipient only.
        setPendingQuestions(Array.isArray(d.pendingQuestions) && d.pendingQuestions.length ? (d.pendingQuestions as Question[]) : null);
        setPendingText(typeof d.pendingText === "string" ? d.pendingText : "");
        setPendingPlan(typeof d.pendingPlan === "string" && d.pendingPlan ? d.pendingPlan : null);
        const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
        // reset: the owner's transcript shrank or was replaced (compaction and the like), so
        // replace. Otherwise merge idempotently keyed by idx, so a resent turn (an assistant turn
        // still growing) or an overlap at a page boundary is not appended twice (see mergeTurns).
        // The tool_use of a question/plan is written to the transcript when it is asked and the
        // answer arrives later as a separate line. When the window straddles those two lines the
        // turn we hold stays unanswered, so answers are patched in afterwards from a whole-
        // transcript map keyed by qid (patchAnswers, as in the mirror). This runs even on a poll
        // that brought no new turn at all — that is exactly the case it exists for.
        const answers = d.answers && typeof d.answers === "object" ? (d.answers as Record<string, { text: string; declined?: boolean }>) : null;
        if (d.reset) setTurns(patchAnswers(incoming, answers));
        else setTurns((old) => patchAnswers(incoming.length ? mergeTurns(old, incoming) : old, answers));
      }
      timer = window.setTimeout(tick, d?.status === "working" ? POLL_WORKING : POLL_IDLE);
    };
    void tick();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [sharedSessionId, refreshList, tr, path]);

  // Handoff proposals live in a store of their own, so they get their own poll (the same shape as
  // the mirror's useHandoffProposals). Riding on the transcript poll would make the CP do two
  // round trips to the owner's Agent every time, doubling the cost of reading a shared history.
  // While the owner's Workspace is stopped they are not fetched, same as the transcript.
  useEffect(() => {
    let live = true;
    seen.current = "";
    setHandoffs([]); // a pane swaps sessions in place, so never flash the previous one's proposals
    const load = async () => {
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (current && current.workspaceState !== "running") return;
      marksReloadRef.current();
      const d = await api(`${path}/handoff-proposals`).catch(() => null);
      if (!live || !d || d.error) return; // a transient failure must not clear the visible cards
      const next = JSON.stringify(Array.isArray(d.proposals) ? d.proposals : []);
      // Unchanged content leaves state alone. Storing a fresh array every time repaints the whole
      // transcript every 5 seconds and runs the tail-following layout effect below for nothing.
      if (next === seen.current) return;
      seen.current = next;
      setHandoffs(JSON.parse(next) as Proposal[]);
    };
    void load();
    const timer = window.setInterval(() => void load(), POLL_HANDOFF);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [sharedSessionId, path]);

  // Remembering the reading position (scrollMark, the mirror's mechanism used as is).
  //
  // First open lands at the tail; returning to a conversation already read lands where it was left.
  // The position is held as a turn ([data-turn-idx]), not px: heights settle late and a revisit
  // refetches the tail, so the same px is not guaranteed to be the same content. It is kept only
  // while the tab lives (a reload forgets it, so the next open is the tail), as in the mirror.
  useEffect(() => {
    restoreMark.current = loadMark(markKey);
    restoring.current = false;
    didInit.current = false;
    interactUntil.current = 0;
    selfTop.current = 0;
    pendingMark.current = null;
    return () => {
      window.clearTimeout(markTimer.current);
      // Swapping to another shared session in the same pane still has the outgoing DOM mounted, so
      // it can be measured again. Closing the pane or switching to one's own session unmounts this
      // whole surface, and by the time this cleanup runs the ref is detached (every rect measures
      // 0). The mirror does not unmount on a swap, so copying only its behaviour loses the position
      // on the most common path of all: leave mid-read, come back.
      saveMark(markKey, captureMark(bodyRef.current, atBottom.current) ?? pendingMark.current);
    };
  }, [markKey]);

  // Keep the module cache in step so the next mount paints from it.
  useEffect(() => {
    if (loaded) {
      transcriptCache.set(sharedSessionId, { turns, cursor: cursor.current, firstLine: firstLine.current, hasMore });
    }
  }, [sharedSessionId, turns, hasMore, loaded]);

  // Older history, one page at a time. The server already supports `before=` (it proxies
  // the query through to the Agent) and the DTO already passes firstLine/hasMore — this
  // just uses them, which is what lets the first fetch stay small.
  const loadOlder = async () => {
    const el = bodyRef.current;
    // "In flight" is tracked in a ref: the loadingOlder state disables the button one render late,
    // so a fast double click fetches the same `before=` twice and stacks the same page twice.
    if (!el || loadingOlderRef.current || firstLine.current <= 0) return;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    const keep = el.scrollHeight - el.scrollTop; // distance from the bottom, held across the prepend
    const d = await api(`${path}/messages?before=${firstLine.current}&limit=${WINDOW}`).catch(() => null);
    if (d && !d.error) {
      const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
      if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
      setHasMore(!!d.hasMore);
      if (incoming.length) {
        anchor.current = keep;
        // Put the older page first and re-merge what we already hold (ascending idx).
        setTurns((old) => mergeTurns(incoming, old));
      }
    }
    loadingOlderRef.current = false;
    setLoadingOlder(false);
  };

  const toBottom = (el: HTMLDivElement) => {
    el.scrollTop = el.scrollHeight;
    selfTop.current = el.scrollTop;
  };

  // Record the position once scrolling settles, since it cannot be measured on the way out. It
  // walks the turns from the top, so do it once after the scrolling stops, not on every event.
  const rememberMark = () => {
    window.clearTimeout(markTimer.current);
    markTimer.current = window.setTimeout(() => {
      pendingMark.current = captureMark(bodyRef.current, atBottom.current);
    }, MARK_SETTLE_MS);
  };

  // Restore the reading position after a prepend, land on open, and otherwise follow the tail.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      selfTop.current = el.scrollTop;
      anchor.current = null;
      return;
    }
    // First landing for this session: the tail if it was left at the tail (or opened for the first
    // time), otherwise the position where reading stopped. If the anchor turn is not in the tail
    // window, give the restore up and fall to the tail, which can be read from again anyway.
    if (!didInit.current) {
      if (!turns.length && !loaded) return; // nothing yet; the next update lands
      didInit.current = true;
      const mark = restoreMark.current;
      if (mark && !mark.atBottom && applyMark(el, mark)) {
        selfTop.current = el.scrollTop;
        atBottom.current = false; // not at the tail, so follow is off and the jump pill shows
        restoring.current = true; // from now on, re-seat to this anchor on every late height
        setShowJump(true);
        rememberMark(); // leaving again without touching anything keeps this position
        return;
      }
      restoreMark.current = null;
      toBottom(el);
      rememberMark();
      return;
    }
    // handoffs are followed too: the cards arrive from a separate poll, so watching only turns
    // leaves us at the tail while the height grows, landing that much too high (measured: +263px).
    if (atBottom.current) toBottom(el);
    // Pending cards are followed for the same reason: their height is added outside the
    // transcript, so watching only turns shifts the landing up the moment a question appears.
  }, [turns, handoffs, loaded, pendingQuestions, pendingPlan, pendingText]);

  // Nearly all of the transcript's height settles late (markdown layout, then highlighting, then
  // image decode, then web fonts). Rather than enumerating the sources, hold the invariant: keep
  // the tail while following and the anchor while restoring, as the mirror does. Without it, an
  // open stops hundreds to thousands of px short of the tail (measured: a 2096px gap).
  useEffect(() => {
    const el = bodyRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (restoring.current) {
        const mark = restoreMark.current;
        if (mark && !atBottom.current && applyMark(el, mark)) {
          selfTop.current = el.scrollTop;
          return;
        }
        endRestore(); // either tail-following resumed (jump to latest) or the anchor is gone
      }
      if (!atBottom.current) return;
      if (Date.now() < interactUntil.current) return; // do not follow what the reader expanded
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 1) return;
      toBottom(el);
    });
    ro.observe(el);
    if (scrollBoxRef.current) ro.observe(scrollBoxRef.current);
    return () => ro.disconnect();
  }, []);

  // Whether to follow is decided by the reader's intent. Judging by raw distance reads the growth
  // right after a self-pin as the reader scrolling up, which stops every later re-pin (measured in
  // the mirror).
  const onScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    rememberMark();
    const movedUp = el.scrollTop < selfTop.current - 1;
    if (atBottom.current && !movedUp) {
      selfTop.current = el.scrollTop;
      setShowJump((s) => (s === false ? s : false));
      return;
    }
    const stuck = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX;
    atBottom.current = stuck;
    if (stuck) selfTop.current = el.scrollTop;
    setShowJump((s) => (s === !stuck ? s : !stuck));
  };

  const endRestore = () => {
    restoring.current = false;
    restoreMark.current = null;
  };

  // A restore is abandoned only on reader input. Never on "scrollTop differs from what I wrote":
  // the browser's scroll anchoring moves it on every late layout, and reading that as a touch
  // sticks short of the destination (measured in the mirror).
  const endRestoreOnInput = () => {
    if (restoring.current) endRestore();
  };

  // Open the window for a reflow the reader caused themselves (expanding the work trace, thinking
  // or a tool run). Both pointer and key are caught in the capture phase, before the reflow.
  const noteInteraction = () => {
    interactUntil.current = Date.now() + INTERACT_HOLD_MS;
    endRestoreOnInput();
  };

  // Jump to latest: go to the tail and resume auto-follow.
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    endRestore();
    atBottom.current = true;
    toBottom(el);
    setShowJump(false);
  };

  const propose = async () => {
    const prompt = draft.trim();
    if (!prompt || sending || !meta) return;
    setSending(true);
    const d = await apiJSON(`api/shared-sessions/${encodeURIComponent(meta.id)}/proposals`, "POST", {
      action: "turn",
      payload: { op: "start", prompt },
    }).catch(() => ({ error: { message: tr("share.proposal_failed") } }));
    setSending(false);
    if (d?.error) setError(errText(d.error));
    else {
      setDraft("");
      setError(tr("share.proposal_sent"));
    }
  };

  const groups = groupTurns(coalesceUserActions(turns));


  // A recipient can read, and nothing else. Every capability the mirror fills in is
  // deliberately absent here — there is no local file to open, no diff pane, no pasted
  // image to fetch from someone else's Workspace, no fork, and no agent of theirs to
  // re-authenticate. The blocks drop those affordances instead of showing dead controls,
  // and fall back to self-contained renderings (tool edits and plans expand in place).
  const caps: TranscriptCaps = {
    agentName: agentOf(meta?.kind).assistantName,
    // The speaker is the owner, not the reader. Left as "you", someone else's conversation would
    // read as if the reader had written it. The name shown is the owner's login id (email
    // address): user_key is a key normalised through sanitizeUser and tells the reader nothing.
    userName: meta && ownerLabel(meta),
    expandThinking: expandThinking(settings, meta?.kind),
    // The one exception: marks are part of the conversation even on a read-only surface, and RW
    // may draw them. They never move the agent, so they do not go through propose-then-approve
    // (ADR 0050 decision 4).
    marks,
  };

  // The shared-session theme and background from the display settings (docs/log/59). Same
  // mechanism as the mirror (.mirrorview): data-theme switches the base tokens for this surface
  // only, and --chat-bg / --chat-accent are derived from this surface's effective theme (carrying
  // the app's colours in verbatim makes them clash on an inverted surface). The point of the
  // setting is that the surface where someone else's conversation is read can differ from one's
  // own mirror.
  const sharedEff = effectiveTheme(settings.sharedTheme, settings.theme);
  const sharedBg = surfaceBg(settings.sharedColor, sharedEff);
  const sharedAccent = surfaceAccent(settings.sharedColor);
  return (
    <div
      className="shared-view"
      data-theme={settings.sharedTheme !== "inherit" ? settings.sharedTheme : undefined}
      style={{
        // Unlike colour, font size and family are not split per surface: legibility is the
        // reader's preference and enlarging only one's own mirror would be pointless. The session
        // mirror settings are passed through as they are (the same contract as MirrorView).
        // Without them the body .mirror-turn .markdown freezes at the CSS fallback of 13.5px /
        // system-ui, and this surface alone ignores the setting.
        "--chat-font": chatFontStack(settings.chatFont),
        "--chat-size": settings.chatSize + "px",
        ...(sharedBg ? { "--chat-bg": sharedBg } : {}),
        ...(sharedAccent ? { "--chat-accent": sharedAccent } : {}),
      } as CSSProperties}
    >
      <header className="shared-view-head">
        <div className="shared-view-info">
          <div>
            <Icon name="broadcast" /> <strong>{meta?.title || meta?.label || meta?.name || tr("share.shared_sessions")}</strong>
          </div>
          {meta && (
            <small>
              {ownerLabel(meta)} · {tr(meta.permission === "rw" ? "share.permission_rw" : "share.permission_ro")} ·{" "}
              {meta.state}
            </small>
          )}
        </div>
        {headerActions && <span className="view-head-actions">{headerActions}</span>}
      </header>
      {/* A handoff addressed to me (docs/log/77). It must not scroll with the body: the transcript
          follows its tail, so placing it inside would push a waiting handoff off screen. Clicking
          opens the inbox narrowed to this one offer, where both accept and decline complete. */}
      {offerId && (
        <div className="shared-view-handoff">
          <Icon name="git-branch" />
          <span className="shared-view-handoff-text">
            <strong>{tr("handoff.banner_title")}</strong>
            {offerFrom && <small>{tr("handoff.from", { who: offerFrom })}</small>}
          </span>
          <Button variant="primary" onClick={() => setOfferOpen(true)}>
            <Icon name="run" /> {tr("handoff.banner_open")}
          </Button>
        </div>
      )}
      {offerOpen && <HandoffInboxModal offerId={offerId} onClose={() => setOfferOpen(false)} />}
      {/* The list of marks (docs/log/69 §69.7), in the same place as the mirror: the band
          directly under the head. */}
      <MarkStrip marks={marks} storageKey={`shared:${sharedSessionId}`} />
      <div
        className="shared-view-body"
        // A surface read by scrolling vertically: horizontal overflow must not kill the sideways
        // swipe (app/swipeGuard.ts).
        data-swipe-y=""
        ref={bodyRef}
        onScroll={onScroll}
        // What abandons a position restore. Wheel and touch are caught here; the pointerdown /
        // keydown below never fire for a wheel.
        onWheelCapture={endRestoreOnInput}
        onTouchStartCapture={endRestoreOnInput}
        tabIndex={-1}
      >
        {/* Inner wrapper whose height equals the transcript. The ResizeObserver watches it and
            re-seats to the tail (or, while restoring, to the anchor) on every late height.
            Expand/collapse gestures are caught here and reported as a reader-caused reflow. */}
        <div
          className="mirror-scroll"
          ref={scrollBoxRef}
          onPointerDownCapture={noteInteraction}
          onKeyDownCapture={noteInteraction}
        >
          {error && <div className="shared-view-notice">{error}</div>}
          {loaded && hasMore && (
            <div className="mirror-loadmore">
              <button type="button" className="ghost mirror-loadmore-btn" disabled={loadingOlder} onClick={() => void loadOlder()}>
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
            !error && (
              <div className="mirror-empty muted mirror-loading">
                <Icon name="loading" spin /> {tr("chat.ph_loading")}
              </div>
            )
          ) : groups.length === 0 && handoffs.length === 0 && !pendingQuestions && !pendingPlan ? (
            // handoffs.length === 0: with an empty transcript the proposal cards are all there is
            // to show, and the empty state skips TranscriptView entirely, so use the mirror's
            // condition.
            <div className="mirror-empty muted">{tr("mirror.no_history")}</div>
          ) : (
            <TranscriptView
              groups={groups}
              caps={caps}
              working={working}
              autoCollapseWork={atBottom.current}
              // Read-only surface, so no edit, discard or launch (never render a control that
              // cannot be pressed). Placed where the mirror places it, at the moment it was
              // proposed: pinned to the tail it would hide the rest of the conversation behind
              // the card forever (see handoffPlacement).
              inlineCards={handoffs.map((h) => ({
                at: h.created_at,
                node: (
                  <HandoffCard key={"handoff-" + h.id} launched={!!h.launched_at} intro={tr("share.handoff_intro")}>
                    <HandoffBody title={h.title} prompt={h.prompt} />
                  </HandoffCard>
                ),
              }))}
            />
          )}
          {/* The modal currently open. It is removed from the transcript and delivered separately
              (see the pendingQuestions note above), so without rendering it here the recipient
              sees nothing while a modal is up. Once it settles the Agent re-emits that line with
              the cursor and it appears in the transcript as a decided card: a swap, not a
              duplicate. */}
          {pendingPlan && (
            <div className="mirror-turn assistant">
              <div className="mirror-turn-head">
                <span className="mt-who">{caps.agentName}</span>
                <span className="mt-model muted">{tr("mirror.plan_pending")}</span>
              </div>
              <div className="mirror-turn-body">
                {/* Approving or rejecting is the owner's call. Passing pending would render those
                    two buttons, so it is not passed (never render a control that cannot be
                    pressed); the body still expands in place. */}
                <PlanBlock plan={pendingPlan} />
              </div>
            </div>
          )}
          {pendingQuestions && (
            <div className="mirror-turn assistant">
              <div className="mirror-turn-head">
                <span className="mt-who">{caps.agentName}</span>
                <span className="mt-model muted">{tr("mirror.questioning")}</span>
              </div>
              <div className="mirror-turn-body">
                {pendingText && <MarkdownView source={pendingText} />}
                {/* The same inert card as a question left in the transcript: options and preview
                    read exactly as they are, only the way to answer is missing (the owner
                    answers). */}
                <QuestionBlock questions={pendingQuestions} />
              </div>
            </div>
          )}
          {showJump && (
            // The mirror's sticky pill (mirror.css). The band has zero height, so it adds no
            // scrollable length. .mirror-jump-row is not decoration but the positioning itself:
            // without it the button sits in flow inside that zero-height band, hugs the left edge
            // and overflows below it (measured: 1px from the left, 13px past the bottom, +24px of
            // scroll). The row (absolute bottom:0 plus centring) restores the mirror's placement:
            // centred, 11px from the bottom, scroll length unchanged.
            <div className="mirror-jump-wrap">
              <div className="mirror-jump-row">
                <button
                  type="button"
                  className="mirror-jump"
                  onClick={jumpToBottom}
                  title={tr("mirror.jump_latest")}
                  aria-label={tr("mirror.jump_latest")}
                >
                  <Icon name="arrow-down" /> {tr("mirror.jump_latest")}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
      {meta?.permission === "rw" && meta.workspaceState === "running" && (
        <div className="shared-propose">
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={tr("share.proposal_placeholder")} />
          <Button variant="primary" icon="send" disabled={!draft.trim() || sending} onClick={() => void propose()}>
            {tr("share.propose")}
          </Button>
          <small>{tr("share.owner_approval_note")}</small>
        </div>
      )}
    </div>
  );
}
