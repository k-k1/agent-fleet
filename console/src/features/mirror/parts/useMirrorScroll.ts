import { useEffect, useRef, useState } from "react";
import { applyMark, captureMark, saveMark, scrollTopForTurn, loadMark, type ScrollMark } from "../scrollMark.ts";
import type { Group } from "../transcript/types.ts";

// The user counts as "stuck to the bottom" (auto-follow on) while within this many px of
// the end. Above it, following stops and the jump-to-latest button appears. Narrower than
// before by request, so follow drops more readily on scroll-up — note this sits close to
// the typing indicator / stop-button row's ~40–60px height swing between polls, so that
// swing can occasionally nudge us out of "at bottom" on its own.
const NEAR_BOTTOM_PX = 80;

// After an interaction inside the transcript, hold off the bottom re-pin for this long, so
// content the READER grew (expanding a work-progress disclosure, switching code wrapping) keeps
// their position instead of snapping past it. Only needs to outlive the reflow the click
// causes — everything else that grows the transcript is content, and is followed.
const INTERACT_HOLD_MS = 600;

// Padding (px) kept above the reply block when "jump to reply top" scrolls to it. At 0 the
// block looks cropped.
const REPLY_TOP_PAD = 8;

/**
 * Everything about the transcript's scroll position: bottom-follow, the re-anchor when a reply
 * completes, position restore, the floating pills, and holding the viewport still when older
 * history is prepended.
 *
 * The caller keeps only the "when":
 *   - `applyFollow()`  the layout effect for a transcript that moved (MirrorView owns the deps)
 *   - `resetForSession()` / `saveMarkFor()` the session-switch layout effect and its teardown
 *   - `armFollow()`    a send — "take me to the conversation"
 *   - `capturePrependHeight()` / `applyPrependAdjust()` prepending older history
 */
export function useMirrorScroll() {
  // Show a "jump to latest ↓" affordance whenever the user has scrolled up off the bottom
  // (auto-follow is paused) so new/streaming content below is discoverable with one click.
  const [showJump, setShowJump] = useState(false);
  // "Jump to reply top" — shown only when the latest reply block's top has scrolled off above
  // the viewport AND follow is off. Never at the bottom, where it would cover the buttons the
  // user has to press (see syncReplyTop).
  const [showReplyTop, setShowReplyTop] = useState(false);
  // Backward paging: the pre-prepend scrollHeight, so we can pin the viewport across it.
  const prependAdjustRef = useRef<number | null>(null);
  const mirrorRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrollBoxRef = useRef<HTMLDivElement>(null); // inner content wrapper — its height tracks the transcript
  // Is auto-follow on (keep the end of the transcript in view)? This tracks the user's
  // INTENT, not raw geometry: it goes false when they actually move the viewport up, and
  // true again when they come back to the end (or send, or press jump-to-latest). Content growing
  // under a pinned viewport is not a reason to drop it — see onBodyScroll.
  const atBottomRef = useRef(true);
  // The scrollTop WE last wrote. onBodyScroll compares against it to tell "content grew
  // under our own pin" (not user intent) from "the user scrolled up" — see there.
  const selfTopRef = useRef(0);
  // Until this ms, a geometry change is attributed to the reader's own click, not to content.
  const interactUntilRef = useRef(0);
  // Position restore (scrollMark): where to return to when this session is reopened. While
  // restoring (restoringRef true) we hold THIS anchor rather than the bottom pin.
  //
  // There is deliberately no time limit, for the same reason as the bottom pin: the height
  // settles late and in several steps, and when the last step lands depends on the machine.
  // Measured (4x throttling / 400 turns), the late layout finished in one large commit and the
  // ResizeObserver fired 3.6s after landing; a 3s cutoff would miss exactly that step and leave
  // the view frozen 24–729px short of the target. The only exits are "the reader touched it"
  // and "follow was re-armed" (send, jump to latest).
  const restoreMarkRef = useRef<ScrollMark | null>(null);
  const restoringRef = useRef(false);
  // The idx of the latest reply block — what "jump to reply top" targets. Written on every
  // render so the closures built with [] (the ResizeObserver, onScroll) can read the current
  // value, as ttsCaptureRef does.
  const lastReplyIdxRef = useRef<number | undefined>(undefined);
  // The idx of the assistant block whose TOP we last brought to the viewport top. A fresh
  // reply is anchored there once (so the user reads it from its first line) and then left
  // alone as it streams; this remembers which reply we've already anchored.
  const anchoredIdxRef = useRef<number | undefined>(undefined);
  // The idx of the reply whose FINAL ANSWER top we've already brought to the viewport top.
  // On completion a following pane collapses the work-progress block into a disclosure, so the
  // reply's top becomes that collapsed row; we then re-anchor once to the final answer's first line
  // (docs/log/24). Kept separate from anchoredIdxRef so the top-anchor and the answer-anchor each
  // fire exactly once per reply.
  const answerAnchoredRef = useRef<number | undefined>(undefined);
  // False until the first content settle for a session. On open we land at the bottom (as
  // before) and mark the reply already present as "seen", so only replies that arrive while
  // the user is watching get anchored to the top — history isn't retro-scrolled.
  const didInitRef = useRef(false);
  // Keep a bottom-stuck view pinned as geometry changes OUTSIDE the poll-driven follow
  // effect: the body's own box resizing (the ToDo / spend / context panels above it, the
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
  // (the ToDo / spend / context panels above it, the composer auto-growing, a pane/window
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
  // work-progress disclosure while parked at the bottom must keep their position, not snap them
  // past what they just opened. That is decided by cause, not by timing: interactUntilRef
  // is armed by an interaction inside the transcript (see the handlers on .mirror-scroll).
  useEffect(() => {
    const el = bodyRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      scheduleReplyTopSync();
      // While restoring, hold the anchor instead of the end — same reason as the bottom pin
      // (height arrives late), only a different target. If atBottomRef is set, something
      // re-armed follow (a send, jump to latest), which wins: fold the restore away.
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
  // the transcript can toggle a disclosure (work progress / thinking / tool run), switch code
  // wrapping, or open a plan comment box — all of which grow the content under a reader who
  // is sitting at the bottom. Both are captured on .mirror-scroll, so the pointer path and
  // the keyboard path (Enter/Space on a <summary>) arm it before the reflow lands. A fold
  // that WE change (foldWork on completion) is content, not interaction, and is followed.
  const noteInteraction = () => {
    interactUntilRef.current = Date.now() + INTERACT_HOLD_MS;
    endRestoreOnInput(); // the reader touched it — their hand outranks the position restore
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

  // Stop restoring (the user touched it, or follow was re-armed). From here on atBottomRef
  // alone decides whether we follow.
  const endRestore = () => {
    restoringRef.current = false;
    restoreMarkRef.current = null;
  };

  // Only real INPUT ends a restore — never the fact that scrollTop drifted from the value we
  // wrote. The browser's own scroll anchoring (it adds to scrollTop by however much the content
  // above grew, to keep the view stable) moves it on every late layout, so reading that as "the
  // user touched it" aborts the restore mid-flight (measured: frozen 354px short of the target
  // and never recovering). With wheel / touch / key / pointer as the only exit, an anchoring
  // drift is always overwritten by the next re-apply.
  //
  // The case this misses is dragging the native scrollbar, for which Chromium dispatches no
  // pointerdown to the element. That tugs against the restore until it folds, but re-grabbing
  // the scrollbar is enough.
  const endRestoreOnInput = () => {
    if (restoringRef.current) endRestore();
  };

  // Jump-to-latest button: snap to the bottom and re-arm auto-follow.
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    endRestore(); // the end was chosen explicitly — drop the restore anchor
    el.scrollTop = el.scrollHeight;
    selfTopRef.current = el.scrollTop;
    atBottomRef.current = true;
    setShowJump(false);
    syncReplyTop();
  };

  // "Jump to reply top" — bring the latest reply block's top to the top of the viewport, so a
  // long answer can be restarted from its beginning in one tap. Hidden while stuck to the
  // bottom (see syncReplyTop).
  //
  // The target is the head of the reply block, not the user's turn: the collapsed work-progress
  // row. That is one step above the completion anchor (answerAnchoredRef, the answer's first
  // line), so the reader can re-read from "what did this reply actually do".
  const jumpToReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    if (!el || idx === undefined) return;
    const top = scrollTopForTurn(el, idx, REPLY_TOP_PAD);
    if (top === null) return;
    endRestore();
    el.scrollTop = top;
    selfTopRef.current = el.scrollTop;
    // We left the end, so follow goes off — otherwise the next poll drags us back down.
    atBottomRef.current = false;
    setShowJump(true);
    syncReplyTop();
  };

  // Showing or hiding the pill (the setState below) must always be deferred to the next frame.
  // Adding or removing DOM in the same frame as the bottom pin breaks the landing: calling this
  // straight from the ResizeObserver or the follow layout effect left the view about one time in
  // four stuck 240px (one image's worth of late layout) short of the end, permanently — the
  // mirror-scroll harness's long scenario goes red. A frame of delay on the pill costs nothing.
  const replyTopSyncRef = useRef(false);
  const scheduleReplyTopSync = () => {
    if (replyTopSyncRef.current) return;
    replyTopSyncRef.current = true;
    requestAnimationFrame(() => {
      replyTopSyncRef.current = false;
      syncReplyTop();
    });
  };

  // Whether "jump to reply top" should be shown — only while the latest reply block's head has
  // scrolled off above the viewport top. If the head is already visible the button would do
  // nothing, so it stays hidden.
  const syncReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    const turn = el && idx !== undefined ? el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`) : null;
    const on = !!(
      el &&
      turn &&
      // Never while stuck to the bottom: the end of the transcript is where the things to press
      // are (the handoff card's launch button, the question / plan / permission answer buttons,
      // copy) and a floating pill over them makes them unclickable. This affordance belongs to
      // reading mid-transcript, i.e. only while follow is off.
      !atBottomRef.current &&
      turn.getBoundingClientRect().top < el.getBoundingClientRect().top - REPLY_TOP_PAD
    );
    setShowReplyTop((s) => (s === on ? s : on));
  };
  // Keep the conversation in view as it grows — but ONLY while the user is stuck to the
  // bottom (atBottomRef). If they've scrolled up to read, we never move them.
  //
  // Runs as a LAYOUT effect: it fires synchronously after the DOM mutates but BEFORE the
  // browser paints or dispatches scroll events. That matters at completion, when the work
  // trace folds into a disclosure and the content height suddenly shrinks — reading/
  // scrolling here first means we set a valid scrollTop before the browser would clamp it
  // and fire a stray scroll (which used to race this effect and mis-place the viewport).
  //
  // While a reply is still WORKING we follow the bottom so the streamed work / answer stays
  // in view. We do NOT strand the user at the end of a long answer, though: the moment the
  // reply COMPLETES we re-anchor once to the FINAL ANSWER's first line at the viewport top
  // (tracked by its idx), so it reads from the start instead of the tail. That upward scroll
  // honestly flips atBottomRef→false via onBodyScroll, so afterwards the user is left alone.
  // Called from MirrorView's layout effect — that is where the deps live.
  const applyFollow = ({
    groups,
    loaded,
    busy,
    pending,
    pendingPlan,
    pendingPerm,
  }: {
    groups: Group[];
    loaded: boolean;
    busy: boolean;
    pending: unknown;
    pendingPlan: string | null;
    pendingPerm: string | null;
  }) => {
    scheduleReplyTopSync(); // re-decide "jump to reply top" on every content change, follow or not
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
        // …except when the session was left part-read: go back there (scrollMark). Leaving at
        // the bottom (atBottom) means "show me the latest", so that still lands at the end. If
        // the anchor's turn is not in the tail window, give the restore up and fall back to the
        // end, which is readable from anyway.
        const mark = restoreMarkRef.current;
        if (mark && !mark.atBottom && applyMark(el, mark)) {
          selfTopRef.current = el.scrollTop;
          atBottomRef.current = false; // not at the end: follow is off and jump-to-latest appears
          restoringRef.current = true; // from now on, re-apply this anchor on every late height
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
      // Still working, a background run (subagent/Workflow) is appending, or we're
      // bridging the idle→reply gap (finalizing) — follow the bottom so the streamed tail
      // (and the typing indicator) stay in view.
      if (busy) {
        toBottom();
        return;
      }
      // Completed: a following pane collapses the work-progress block into a disclosure
      // (defaultWorkOpen=!atBottom, and we've been at the bottom) so the reply's top becomes that
      // collapsed row —
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
  };

  // A send or a jump-to-latest is the user saying "take me to the conversation" — re-arm follow.
  const armFollow = () => {
    atBottomRef.current = true;
    setShowJump(false);
  };

  // Reset on a session switch, run inside MirrorView's layout effect in that effect's order.
  const resetForSession = (session: string) => {
    atBottomRef.current = true; // a freshly opened session starts pinned to the bottom
    // The old scroller can be reused for another session (pane D&D / opening a row
    // in the current mirror). Clear its physical offset in the same pre-paint phase;
    // the first transcript layout effect then pins the new content to its end.
    if (bodyRef.current) { bodyRef.current.scrollTop = 0; selfTopRef.current = 0; }
    // The "the reader grew this" window belongs to the PREVIOUS session and must not carry over.
    // Switching sessions with a horizontal swipe on a phone drops that finger's pointerdown on
    // the transcript and arms noteInteraction for 600ms (the capture handler on .mirror-scroll);
    // since almost all of the mirror's height arrives late, a window still open swallows the
    // ResizeObserver's re-pin and the view can settle somewhere short of the end.
    //
    // To be honest this closes a hole rather than a reproduced bug: in the mirror-scroll swipe
    // scenario the view landed at the end with or without this line, because fetch plus render
    // always took longer than 600ms. It only matters on a machine fast enough for the window to
    // still be open.
    interactUntilRef.current = 0;
    // Where this session was last read, if anywhere. The restore itself waits until the
    // transcript is mounted (the first settle); until then the bottom pin holds.
    restoreMarkRef.current = loadMark(session);
    restoringRef.current = false;
    setShowJump(false); // …so no jump-to-latest affordance until they scroll up
    setShowReplyTop(false); // nothing to jump to until the new session's reply is mounted
    anchoredIdxRef.current = undefined; // no reply anchored yet in the new session
    answerAnchoredRef.current = undefined; // …nor its final answer
    didInitRef.current = false; // re-run the "land at bottom on open" settle for this session
  };

  /** Drop a pending prepend adjustment, at the head of a session switch with the other cursors. */
  const resetPrepend = () => {
    prependAdjustRef.current = null;
  };

  /** Record the position being read on leave; the DOM the cleanup reads is the OUTGOING one. */
  const saveMarkFor = (session: string) => saveMark(session, captureMark(bodyRef.current, atBottomRef.current));

  /** Record the height just before older history is prepended, so scrollTop can be advanced by
   *  exactly that much and the viewport held. */
  const capturePrependHeight = () => {
    const el = bodyRef.current;
    prependAdjustRef.current = el ? el.scrollHeight : null; // pin the viewport across the prepend
  };
  // After an older page is prepended, restore the viewport: scrollTop grows by exactly the
  // height added on top, so the user stays on the same content instead of jumping up.
  const applyPrependAdjust = () => {
    const el = bodyRef.current;
    if (el && prependAdjustRef.current != null) {
      el.scrollTop += el.scrollHeight - prependAdjustRef.current;
      selfTopRef.current = el.scrollTop;
      prependAdjustRef.current = null;
    }
  };

  return {
    mirrorRef,
    bodyRef,
    scrollBoxRef,
    atBottomRef,
    lastReplyIdxRef,
    showJump,
    showReplyTop,
    applyFollow,
    armFollow,
    resetForSession,
    resetPrepend,
    saveMarkFor,
    capturePrependHeight,
    applyPrependAdjust,
    noteInteraction,
    onBodyScroll,
    endRestoreOnInput,
    jumpToBottom,
    jumpToReplyTop,
  };
}
