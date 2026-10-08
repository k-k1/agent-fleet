import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { api, parseDiscards, parseQueueItems } from "../../../core/api/client.ts";
import type { CarriedInteraction } from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { loadMark, onJump, type ScrollMark } from "../scrollMark.ts";
import { reachTurn, type ReachOutcome } from "../reachTurn.ts";
import { MIRROR_POLL_FAST, pollDelay } from "../pollCadence.ts";
import { echoNeedsResync } from "../pendingEcho.ts";
import { type InteractionAnswerWire, patchAnswers } from "../interactionAnswers.ts";
import { emptyDiscardNotices } from "../stopQueue.ts";
import type { Turn } from "../transcript/types.ts";
import { isPendingApproval } from "../transcript/types.ts";
import { parsePendingPeers } from "./PendingPeersNotice.tsx";
import { echoStore } from "./sendEcho.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { useMirrorScroll } from "./useMirrorScroll.ts";
import type { useMirrorTts } from "./useMirrorTts.tsx";

const q = encodeURIComponent;

// Transcript window size (jsonl lines) for the initial tail load and each backward page.
// The server clamps it; matches docs/decisions/0009 (P2).
const WINDOW = 400;

/**
 * useTranscriptPoll owns how the mirror's transcript arrives: the per-session reset and the
 * poll loop that fills MirrorState. MirrorView calls it right after the scroll / TTS / marks /
 * translate hooks: the reset must run after their effects and before the poll starts.
 */
export function useTranscriptPoll({
  session,
  sessionMeta,
  st,
  scroll,
  tts,
  marksReloadRef,
}: {
  session: string;
  sessionMeta?: Session | null;
  st: MirrorState;
  scroll: ReturnType<typeof useMirrorScroll>;
  tts: ReturnType<typeof useMirrorTts>;
  marksReloadRef: RefObject<() => void>;
}) {
  const {
    setTurns, setStateSession, setPendingSends, pendingSendsRef, applyEchoes, setLoaded, setTermState,
    setCompactProg, setStatus, setBgBusy, setBgBusyReason, setFinalizing, finalizingRef, wasWorkingRef,
    setTasks, setFiles, setQueuedPrompts, setQueuedItems, setDiscards, setDiscardNotices, setPendingPeers,
    queueOpsRef, setAlive, setPending, setPendingText, liveOnRef, setLiveText, setPendingPlan, setPendingPerm,
    setPendingApproval, setCarried, rejectedPlansRef, setMode, lastNonPlanMode, setAgentCtx,
    setSuggestedTitle, setTitleActing, setManagedSettingsOpen, setManagedSettings, setPasting, setLightbox,
    setHistIdx, cursorRef, firstLineRef, setHasMore, setLoadingOlder, loadingOlderRef, diagRef, statusRef,
    bgBusyRef, lastPayloadRef, unchangedRef, lastPollAtRef, aggSigRef, tickRef,
  } = st;

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
    lastPayloadRef.current = ""; // another session's payload must never read as "unchanged"
    unchangedRef.current = 0;
    aggSigRef.current = ""; // the aggregates belong to the session being left
    setTurns([]);
    setPendingSends(echoStore.get(session) ?? []); // restore this session's un-landed echoes
    pendingSendsRef.current = echoStore.get(session) ?? [];
    rejectedPlansRef.current = new Set(); // optimistic reject marks belong to the old session
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
    setQueuedItems(null);
    setDiscards([]);
    setDiscardNotices(emptyDiscardNotices);
    setPendingPeers([]);
    queueOpsRef.current = new Set();
    setAlive(!!sessionMeta?.alive);
    setPending(null);
    setLiveText(""); // the reply being written belongs to the session being left
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
  // it while visible). Faster while claude is working, slower at rest, and easing off
  // further the longer the payload repeats itself (pollCadence.ts). New turns are
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
      lastPollAtRef.current = Date.now(); // gap the reader's bump measures against
      try {
        // First poll: fetch only the TAIL window (fast on huge transcripts); the server
        // returns firstLine/hasMore so we can page older history in on scroll. Subsequent
        // polls are plain since=<cursor> increments (unchanged).
        const first = cursorRef.current === 0;
        // agg=<digest>: "I already hold these aggregates". Only on the incremental poll — a
        // windowed read brings turns we have never patched with answers, and the Agent ignores
        // the parameter there for that reason.
        const agg = !first && aggSigRef.current ? `&agg=${encodeURIComponent(aggSigRef.current)}` : "";
        // live=1 asks for the reply still being written. Left off when the setting is, so the
        // request and its response stay exactly what they were before the setting existed.
        const live = liveOnRef.current ? "&live=1" : "";
        const url = first
          ? `api/sessions/${q(session)}/messages?since=0&tail=1&limit=${WINDOW}${live}`
          : `api/sessions/${q(session)}/messages?since=${cursorRef.current}${agg}${live}`;
        const d = await api(url);
        if (!alive) return;
        // Refreshing marks rides the transcript poll rather than adding a cycle of its own;
        // useMarksController throttles the actual round trips.
        marksReloadRef.current();
        if (d && !d.error) {
          // Nothing moved since the last poll. Applying the payload anyway is what made a
          // mirror re-render once a second while the agent merely thought: setTasks/setFiles/
          // setQueuedPrompts hand React a NEW array every time, so the state always "changes"
          // and the whole conversation is regrouped and re-rendered for no difference at all.
          // Comparing the payload verbatim is what makes the skip safe — the block below is
          // pure state application, so replaying identical bytes cannot produce a different
          // result. The liveness self-heal after it is time-based, so it stays outside.
          const payload = JSON.stringify(d);
          if (payload === lastPayloadRef.current) {
            unchangedRef.current++; // eases the cadence off (pollCadence.ts)
          } else {
            lastPayloadRef.current = payload;
            unchangedRef.current = 0;
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
            // aggSame: the Agent confirmed the aggregates we hold are current and sent none of
            // them, so leaving the state alone IS applying the response. Overwriting with the
            // absent fields would clear the file strip and the ToDo list on every poll.
            if (typeof d.aggSig === "string") aggSigRef.current = d.aggSig;
            if (d.aggSame !== true) {
              setTasks(Array.isArray(d.tasks) ? d.tasks : []);
              setFiles(Array.isArray(d.files) ? d.files : []);
            }
            setQueuedPrompts(Array.isArray(d.queuedPrompts) ? d.queuedPrompts : []);
            setQueuedItems(parseQueueItems(d.queuedItems));
            // The poll, not the interrupt's answer, is what the notice trusts: an answer lost
            // to a closed tab or a dropped connection comes back here (decision 4).
            setDiscards(parseDiscards(d.discardedInputs));
            setPendingPeers(parsePendingPeers(d.pendingPeers));
            setPending(Array.isArray(d.pendingQuestions) ? d.pendingQuestions : null);
            setPendingText(typeof d.pendingText === "string" ? d.pendingText : "");
            setLiveText(liveOnRef.current && typeof d.liveText === "string" ? d.liveText : "");
            setPendingPlan(typeof d.pendingPlan === "string" && d.pendingPlan ? d.pendingPlan : null);
            setPendingPerm(typeof d.pendingPermission === "string" && d.pendingPermission ? d.pendingPermission : null);
            setPendingApproval(isPendingApproval(d.pendingApproval) ? d.pendingApproval : null);
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
          }
          // Self-heal an unreconciled echo that can no longer land because the turn it
          // should match never reached us (a cursor handed out past a turn we then never
          // asked for again). Only while the session is at rest — a pending echo is
          // normal and expected mid-turn — and once per echo: rewind the cursor so the
          // next tick re-reads the tail window from scratch, which fills the hole and lets
          // the echo land. If the prompt genuinely never arrived, nothing changes and the
          // badge keeps telling the truth. Runs on an unchanged poll too: the condition is
          // elapsed time, and an echo stuck behind a hole is exactly a payload that repeats.
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
      timer = setTimeout(
        tick,
        pollDelay({
          working: statusRef.current === "working" || bgBusyRef.current || finalizingRef.current,
          unchanged: unchangedRef.current,
        }),
      );
    };
    tickRef.current = () => {
      if (timer) clearTimeout(timer);
      tick();
    };
    const onVisible = () => {
      if (!document.hidden) tickRef.current?.();
    };
    // Someone touching the pane is the one signal the payload cannot give: they are watching
    // THIS session now, so drop back to the fast rung and re-read immediately. Guarded by a
    // minimum gap so a scroll or a burst of typing cannot turn into a request per event, and
    // by the streak so the common case (already fast) costs nothing.
    const bump = () => {
      if (unchangedRef.current === 0) return;
      if (Date.now() - lastPollAtRef.current < MIRROR_POLL_FAST) return;
      unchangedRef.current = 0;
      tickRef.current?.();
    };
    const root = scroll.mirrorRef.current;
    document.addEventListener("visibilitychange", onVisible);
    root?.addEventListener("pointerdown", bump, { passive: true });
    root?.addEventListener("keydown", bump);
    root?.addEventListener("scroll", bump, { passive: true, capture: true });
    tick();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
      tickRef.current = null;
      document.removeEventListener("visibilitychange", onVisible);
      root?.removeEventListener("pointerdown", bump);
      root?.removeEventListener("keydown", bump);
      root?.removeEventListener("scroll", bump, { capture: true });
    };
  }, [session]);
}

/**
 * useOlderHistory pages older history in above the oldest line held (P2), both from the button
 * and from the top sentinel scrolling into view. Returns the loader for the button.
 */
export function useOlderHistory({
  session,
  st,
  scroll,
  toast,
}: {
  session: string;
  st: MirrorState;
  scroll: ReturnType<typeof useMirrorScroll>;
  toast: (msg: string) => void;
}) {
  const { turns, setTurns, firstLineRef, hasMore, setHasMore, setLoadingOlder, loadingOlderRef, topSentinelRef, loaded, stateSession } = st;
  const { bodyRef } = scroll;
  // Bumped whenever the transcript this hook serves changes under an async fetch (session switch,
  // unmount): a page, a toast or a jump that started under an older value is dropped, and so is the
  // right to release the shared loading flag.
  const lifeRef = useRef(0);
  useLayoutEffect(() => {
    lifeRef.current++;
    wantRef.current = null;
    readyRef.current = null;
    // The previous session's fetch can no longer release these (its life is over), so the new
    // session starts with the lock free. The poll's own reset does the same for the flag.
    loadingOlderRef.current = false;
    setLoadingOlder(false);
    return () => {
      lifeRef.current++;
    };
  }, [session]);
  // The first window of THIS session is held (not the previous session's state, on the commit where
  // the prop changes). Read from effects and callbacks built earlier, hence a ref.
  const windowHeldRef = useRef(false);
  windowHeldRef.current = loaded && stateSession === session;
  const turnsRef = useRef(turns);
  turnsRef.current = turns;
  // The newest explicit jump (palette hit, ADR 0110) still to be served; `jumpGenRef` names it, so
  // a run that started for an earlier one finds out and stands down. `ready` is the mark to apply
  // once its pages are mounted.
  const wantRef = useRef<ScrollMark | null>(null);
  const readyRef = useRef<ScrollMark | null>(null);
  // The reader's input count and place when the jump was ACCEPTED: input while it waits (for the
  // first window, or for the lock) counts against it as much as input while its pages load.
  const wantAtRef = useRef<{ seq: number; place: ReturnType<typeof scroll.placeSnapshot> } | null>(null);
  const jumpGenRef = useRef(0);
  // The life a jump run belongs to (0 = none). A run of an older life never blocks a new one.
  const runningRef = useRef(0);
  // The newest render's serveJumps. A fetch that outlived its session hands over through this one:
  // its own closure still names the old session in every URL it builds.
  const serveRef = useRef<() => Promise<void>>(async () => {});
  const [jumpGo, setJumpGo] = useState(0);
  // The oldest idx held, lowered by every page fetched (turnsRef lags a render behind a prepend).
  const oldestRef = useRef(Infinity);

  // Fetch the page before the oldest line we hold and prepend it. false when it failed or the
  // transcript changed under it (the turns then belong to another one and are dropped).
  const pageOnce = async (limit: number): Promise<boolean> => {
    const life = lifeRef.current;
    try {
      const before = firstLineRef.current;
      const d = await api(`api/sessions/${q(session)}/messages?before=${before}&limit=${limit}`);
      if (life !== lifeRef.current) return false;
      if (d && !d.error && Array.isArray(d.messages)) {
        if (d.messages.length) {
          scroll.capturePrependAnchor(); // keep the viewport steady across the prepend
          const older = d.messages;
          for (const t of older) if (typeof t.idx === "number" && t.idx < oldestRef.current) oldestRef.current = t.idx;
          setTurns((t) => [...older, ...t]);
        }
        if (typeof d.firstLine === "number") firstLineRef.current = d.firstLine;
        setHasMore(!!d.hasMore);
        return true;
      }
    } catch {
      /* transient — the user can trigger again */
    }
    return false;
  };

  // Page older history in (P2). The loading flag is the one lock for the button, the observer and
  // a jump; whoever releases it hands over to a jump that arrived meanwhile.
  const release = (life: number) => {
    if (life === lifeRef.current) {
      loadingOlderRef.current = false; // else the session changed: the flag is the new one's now
      setLoadingOlder(false);
    }
    void serveRef.current();
  };
  const loadOlder = async () => {
    if (loadingOlderRef.current || firstLineRef.current <= 0) return;
    const life = lifeRef.current;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    try {
      await pageOnce(WINDOW);
    } finally {
      release(life);
    }
  };

  // Serve the newest jump, one at a time. A jump that arrives during a run supersedes it (the run
  // sees the generation move and stops between pages) and is served when the lock frees.
  const serveJumps = async () => {
    if (runningRef.current === lifeRef.current || loadingOlderRef.current || !windowHeldRef.current) return;
    const mark = wantRef.current;
    if (!mark) return;
    wantRef.current = null;
    const at = wantAtRef.current;
    wantAtRef.current = null;
    const seq = at ? at.seq : scroll.inputSeqRef.current;
    const place = at ? at.place : scroll.placeSnapshot();
    if (scroll.inputSeqRef.current !== seq || scroll.placeMoved(place)) return; // the reader took over while it waited
    const gen = jumpGenRef.current;
    const life = lifeRef.current;
    runningRef.current = life;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    oldestRef.current = Math.min(Infinity, ...turnsRef.current.map((t) => (typeof t.idx === "number" ? t.idx : Infinity)));
    let outcome: ReachOutcome = "cancelled";
    try {
      const stale = () => life !== lifeRef.current || gen !== jumpGenRef.current;
      outcome = await reachTurn(mark.idx, {
        oldestIdx: () => oldestRef.current,
        exhausted: () => firstLineRef.current <= 0,
        cursor: () => firstLineRef.current,
        page: pageOnce,
        cancelled: () => stale() || scroll.inputSeqRef.current !== seq || scroll.placeMoved(place),
      });
      if (stale()) outcome = "cancelled";
      else if ((outcome === "reached" || outcome === "mounted") && (scroll.inputSeqRef.current !== seq || scroll.placeMoved(place))) outcome = "cancelled";
      if (outcome === "too-far") toast(tr("mirror.jump_unreachable"));
      else if (outcome === "failed") toast(tr("mirror.jump_failed"));
      else if (outcome === "reached" || outcome === "mounted") {
        // "mounted" too: a hit that the page of ANOTHER fetch (the button's) brought in was not
        // there when the mirror's own listener tried it.
        readyRef.current = mark;
        setJumpGo((n) => n + 1);
      }
    } finally {
      if (runningRef.current === life) runningRef.current = 0;
      release(life);
    }
  };

  serveRef.current = serveJumps;

  // A hit for the session shown here. Listens next to useMirrorScroll's own, which restores the
  // turn when it is mounted and gives up quietly when it is not — the case handled here.
  useEffect(() => {
    const take = (m: ScrollMark | null) => {
      if (!m || !m.near || m.atBottom) return;
      jumpGenRef.current++; // supersedes a run in flight
      wantRef.current = m;
      wantAtRef.current = { seq: scroll.inputSeqRef.current, place: scroll.placeSnapshot() };
      void serveJumps();
    };
    take(loadMark(session));
    return onJump((sess, m) => {
      if (sess === session) take(m);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- serveJumps reads live refs
  }, [session]);
  useEffect(() => {
    void serveJumps();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, stateSession]);
  useLayoutEffect(() => {
    const mark = readyRef.current;
    if (!mark) return;
    readyRef.current = null;
    if (!scroll.jumpTo(mark)) toast(tr("mirror.jump_failed"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jumpGo]);

  // Put the reader back on the turn they were reading across the prepend. The hold then stays
  // armed inside useMirrorScroll, because at this point the prepended turns have no content yet.
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

  return loadOlder;
}
