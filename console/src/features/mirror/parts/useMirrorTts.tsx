import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { SelectionFloat } from "../../../ui/SelectionFloat.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { useSelectionCapture } from "../../../lib/selectionCapture.ts";
import { displayName } from "../../../lib/sessionview.ts";
import type { Session } from "../../../types/session.ts";
import type { Settings } from "../../../lib/settings.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import {
  sessionVoiceOpts,
  announce,
  onTtsStop,
  startTts,
  stopTtsForReplacement,
  ttsOptsFromSettings,
  workVoiceOpts,
  type TtsController,
} from "../../chat/tts.ts";
import { pendingSpeech } from "../../chat/ttsText.ts";
import { askAssistant } from "../../chat/api.ts";
import {
  readTurn,
  collectBlocks,
  finalAnswerStart,
  blockIndexAt,
  turnSpokenText,
  claimTurnReader,
  isTurnReader,
  type TurnReadHandle,
} from "../turnTts.ts";
import { confirmedWorkEnd, latestWorkPromptIndex, textOfParts } from "../mirrorParts.ts";
import type { Group, Question, Turn, TurnTtsWiring } from "../transcript/types.ts";

/**
 * All of the mirror's read-aloud: karaoke reading, auto read-aloud, the quiet reading of work in
 * progress, announcing a confirmation, and the "read from here" pill.
 *
 * The caller keeps only the three whose "when" belongs to MirrorView's context:
 *   - `resetForSession()` — from the session-switch layout effect (the order there matters)
 *   - `resetForTranscript()` — from the branch where polling received a `reset`
 *   - `syncAutoRead()` — from the effect for a moved transcript (the caller owns the deps)
 */
export function useMirrorTts({
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
}: {
  session: string;
  sessionMeta?: Session | null;
  paneId: string;
  active?: boolean;
  readOnly: boolean;
  settings: Settings;
  bodyRef: RefObject<HTMLDivElement | null>;
  statusRef: RefObject<string>;
  loaded: boolean;
  pending: Question[] | null;
  pendingPlan: string | null;
  pendingPerm: string | null;
}) {
  // --- Karaoke reading (turnTts, docs/log/24) ---------------------------------------
  // The turn being read (its transcript idx) and whether it is paused. onEnd (natural end, a
  // TopBar stop, another playback starting) clears only our own entry.
  const [ttsReading, setTtsReading] = useState<{ idx: number; paused: boolean } | null>(null);
  const ttsHandleRef = useRef<TurnReadHandle | null>(null);
  // The pill that reads from the selection, following ReaderView's "read from here" pattern.
  const [ttsPill, setTtsPill] = useState<{ x: number; y: number; idx: number; body: HTMLElement; block: number } | null>(
    null,
  );
  // Auto read-aloud (P2): the baseline idx (nothing older than it is read), the queue of group
  // idxs still to read, and the per-group count of blocks already read — a group grows by
  // appending, so only the increment is read.
  const ttsAutoSeenRef = useRef<number | null>(null);
  // Which session the baseline above belongs to. The baseline is a bare jsonl line number, so it
  // is meaningless in another session. A pane drag-and-drop swap keeps the same instance and only
  // replaces the session prop (and makes the drop target active), so the auto-read effect can run
  // while the previous session's turns are still mounted and build a baseline from those line
  // numbers — after which the new session's body looks "new" and its last final answer gets read
  // out unasked. This guard keeps us re-baselining until the session matches.
  const ttsAutoSessionRef = useRef(session);
  const ttsAutoQueueRef = useRef<number[]>([]);
  const ttsAutoDoneRef = useRef(new Map<number, number>());
  // Quiet reading of work already confirmed. Progress is kept as a part index, and only text
  // settled before the last tool/question/plan is read. When the final answer arrives (idle) the
  // whole queue is dropped so ordinary reading takes over.
  const ttsWorkRef = useRef<TtsController | null>(null);
  const ttsWorkQueueRef = useRef<string[]>([]);
  const ttsWorkDoneRef = useRef(new Map<number, number>());
  // Claim the reader role (turnTts.ts). With the same session open in several panes only the
  // first claimant reads. A readOnly (unattached) pane never reads, so it does not claim.
  const ttsTokenRef = useRef(Symbol("ttsReader"));
  useEffect(() => {
    if (readOnly) return;
    return claimTurnReader(session, ttsTokenRef.current);
  }, [session, readOnly]);
  // An explicit stop (TopBar, footer, etc., but not a preemption) means "be quiet", so our own
  // auto-read queue is dropped too. With all-pane reading, a stop from another pane arrives here
  // as well.
  useEffect(
    () =>
      onTtsStop(() => {
        ttsAutoQueueRef.current.length = 0;
        ttsWorkQueueRef.current.length = 0;
      }),
    [],
  );
  const ttsStart = (idx: number, body: HTMLElement, fromBlock = 0) => {
    ttsHandleRef.current?.stop("replaced"); // an internal replacement: keep the auto-read queue
    const h = readTurn(
      body,
      sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback"),
      fromBlock,
      (reason) => {
        ttsHandleRef.current = null;
        setTtsReading((cur) => (cur?.idx === idx ? null : cur));
        // Only an explicit stop by the user drops the queue. Being replaced by another playback
        // keeps it, and the resume decision runs from a microtask so it sees the state after the
        // replacement has registered as active.
        if (reason === "explicit") ttsAutoQueueRef.current.length = 0;
        else queueMicrotask(() => ttsAutoPumpRef.current());
      },
      { ...(sessionVoiceOpts(session) ?? {}), paneId }, // session voice + the origin pane's stereo position
      session, // for the "playing" icon in the left rail
    );
    if (!h) return; // nothing readable in this turn (a tool-only turn, say)
    ttsHandleRef.current = h;
    setTtsReading({ idx, paused: false });
  };
  // Summary reading of a long answer (the ttsSummaryRead setting). New content longer than this
  // many characters is not read in full: the assistant (a headless CLI one-shot with no tools)
  // summarises it in two sentences and that is read instead.
  const TTS_SUMMARY_MIN = 500;
  // i18n-exempt-start: LLM prompt (model behaviour, not display; docs/log/28 §4)
  const TTS_SUMMARY_PROMPT =
    "次のテキストはコーディングエージェントの回答です。音声で聞くための要約を、日本語で最大2文・120字以内で書いてください。" +
    "記号・コード・URL・箇条書きは使わず、プレーンな文章だけを返してください。要約以外の前置きや説明は書かないでください。\n\n---\n";
  // i18n-exempt-end
  const ttsSummaryBusyRef = useRef(false); // a summary is being generated (one at a time; the queue waits)

  // Generate the summary and read it through announce (a serial queue that waits for playback to
  // free up, integrated with the TopBar stop). No karaoke highlight, because the summary text is
  // not on screen — the full body can always be karaoke-read from the footer button. A failure or
  // a timeout falls back to reading the full text.
  const ttsSummarize = async (gi: number, body: HTMLElement, fromBlock: number, text: string) => {
    const label = (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.summary_suffix");
    try {
      const r = await Promise.race([
        askAssistant(TTS_SUMMARY_PROMPT + text.slice(0, 6000)),
        new Promise<never>((_, rej) => setTimeout(() => rej(new Error("timeout")), 30000)),
      ]);
      const reply = (r?.reply || "").trim();
      if (!r?.error && reply)
        announce(tr("mirror.tts.summary_prefix") + reply, label, { ...(sessionVoiceOpts(session) ?? {}), paneId }, session);
      else ttsStart(gi, body, fromBlock); // no summary came back -> read the full text
    } catch {
      ttsStart(gi, body, fromBlock); // workspace stopped, timeout, etc. -> read the full text
    } finally {
      ttsSummaryBusyRef.current = false;
      ttsAutoPumpRef.current(); // let the waiting ones through (if playing, the speaking release resumes)
    }
  };

  // Read the not-yet-read blocks of the queue's head. If anything is playing (us, a chat reading,
  // an announcement) we wait — resumption is triggered by onEnd and by the speaking release (the
  // subscribe below).
  const ttsAutoPump = () => {
    if (!settings.ttsEnabled || !settings.ttsAutoReadMirror) {
      ttsAutoQueueRef.current.length = 0;
      return;
    }
    // Deciding "is this the final answer" from a mid-poll body starts reading the work in
    // progress whenever the narration runs one poll ahead of the tool trace. So we queue until
    // the work is done and, once status leaves working, read only what follows the last tool in
    // the finished DOM.
    if (statusRef.current === "working") return;
    if (ttsSummaryBusyRef.current) return; // a summary is being generated: resume in order after it
    // Wait while anything is playing or getting ready. Checking speaking alone would cut into a
    // playback still waiting on synthesis (registered, first sound not out yet), so active is
    // checked too — this is what serialises the pumps of the other panes in all-pane reading.
    const st = useTtsStore.getState();
    if (ttsHandleRef.current || st.speaking || st.active) return;
    const q = ttsAutoQueueRef.current;
    while (q.length) {
      const gi = q.shift()!;
      const body = bodyRef.current?.querySelector<HTMLElement>(`[data-turn-idx="${gi}"] .mirror-turn-body`);
      if (!body) continue; // a turn that disappeared (a reset, say)
      const done = ttsAutoDoneRef.current.get(gi) ?? 0;
      const total = collectBlocks(body).length;
      ttsAutoDoneRef.current.set(gi, total);
      if (total <= done) continue; // no increment (a tool-only append, say)
      // Skip the work in progress, in the same spirit as chat's separation (docs/log/19): in the
      // finished body, jump past the pre-tool narration and auto-read only what follows the last
      // tool, i.e. the final answer. After completion the work moves inside a disclosure, so
      // manual reading — which reads the direct DOM children — lands on the final answer too.
      const from = Math.max(done, finalAnswerStart(body));
      if (total <= from) continue; // no final-answer block to read yet (only work was appended)
      if (settings.ttsSummaryRead) {
        const text = turnSpokenText(body, from);
        if (text.length > TTS_SUMMARY_MIN) {
          ttsSummaryBusyRef.current = true;
          void ttsSummarize(gi, body, from, text);
          return;
        }
      }
      ttsStart(gi, body, from);
      if (ttsHandleRef.current) return; // reading started (with nothing readable, try the next one)
    }
  };
  const ttsAutoPumpRef = useRef(ttsAutoPump);
  ttsAutoPumpRef.current = ttsAutoPump;
  const ttsWorkPump = () => {
    if (!settings.ttsEnabled || !settings.ttsAutoReadMirror || settings.ttsWorkRead === "off") {
      ttsWorkQueueRef.current.length = 0;
      return;
    }
    if (statusRef.current !== "working" || ttsWorkRef.current) return;
    const st = useTtsStore.getState();
    if (st.active || st.speaking) return; // never cut into important playback (final answer, announcement)
    const text = ttsWorkQueueRef.current.shift();
    if (!text) return;
    const voice = { ...(sessionVoiceOpts(session) ?? {}), paneId };
    const c = startTts(
      { ...ttsOptsFromSettings(settings), ...voice, ...workVoiceOpts(voice, settings.ttsWorkRead) },
      (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.work_suffix"),
      (reason) => {
        ttsWorkRef.current = null;
        if (reason === "explicit") ttsWorkQueueRef.current.length = 0;
        else queueMicrotask(() => ttsWorkPumpRef.current());
      },
      session,
    );
    ttsWorkRef.current = c;
    c.push(text);
    c.flush();
  };
  const ttsWorkPumpRef = useRef(ttsWorkPump);
  ttsWorkPumpRef.current = ttsWorkPump;
  // When another playback ends and the voice frees up, resume the auto-read we held back.
  // zustand's subscribe runs synchronously inside setState, and mid-preemption (old playback
  // stopped, new one not yet registered) active is briefly null — so the decision is deferred to
  // a microtask and made on the state after the replacement completes.
  useEffect(() => {
    return useTtsStore.subscribe((st, prev) => {
      if (prev.speaking && !st.speaking)
        queueMicrotask(() => {
          ttsWorkPumpRef.current();
          ttsAutoPumpRef.current();
        });
    });
  }, []);

  // Reading confirmations and questions (the ttsReadPending setting): read a pending
  // AskUserQuestion / plan approval / permission request when it NEWLY appears. Active pane only,
  // or every open pane under all-pane reading (ttsAutoReadAllPanes); a session with no pane is
  // covered by useSessionNotifications' short announcement. Anything already pending when the
  // pane opened is swallowed as the baseline and not read, so moving between panes does not
  // re-read it.
  const ttsPendingInitRef = useRef(false);
  const ttsPendingSigRef = useRef("");
  useEffect(() => {
    if (!loaded) return;
    const sig = pending
      ? "q:" + JSON.stringify(pending)
      : pendingPlan
        ? "plan:" + pendingPlan.slice(0, 200)
        : pendingPerm
          ? "perm:" + pendingPerm
          : "";
    if (!ttsPendingInitRef.current) {
      ttsPendingInitRef.current = true;
      ttsPendingSigRef.current = sig;
      return;
    }
    if (sig === ttsPendingSigRef.current) return;
    ttsPendingSigRef.current = sig;
    if (!sig || readOnly) return;
    // Same pane rule as auto read-aloud: the active pane, or the claimed pane under all-pane reading.
    if (settings.ttsAutoReadAllPanes ? !isTurnReader(session, ttsTokenRef.current) : !active) return;
    if (!settings.ttsEnabled || !settings.ttsReadPending) return;
    const label = (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.confirm_suffix");
    const text = pending
      ? pendingSpeech(pending)
      : pendingPlan
        ? tr("mirror.tts.plan_ready")
        : tr("mirror.tts.permission_wait") + (pendingPerm || "").slice(0, 100);
    announce(text, label, { ...(sessionVoiceOpts(session) ?? {}), paneId });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, pending, pendingPlan, pendingPerm]);
  const ttsWiring: TurnTtsWiring = {
    reading: ttsReading,
    start: ttsStart,
    pause: () => {
      ttsHandleRef.current?.pause();
      setTtsReading((c) => (c ? { ...c, paused: true } : c));
    },
    resume: () => {
      ttsHandleRef.current?.resume();
      setTtsReading((c) => (c ? { ...c, paused: false } : c));
    },
    stop: () => ttsHandleRef.current?.stop(), // the cleanup happens in onEnd
  };
  // Stop on a session switch, because the body DOM is replaced with it. Do NOT stop on unmount
  // (switching to the terminal, closing the pane): playback is a single global stream that does
  // not depend on the view, so it keeps running and the TopBar stop is control enough. The
  // karaoke highlight is simply discarded with the detached DOM, which is harmless — it is not
  // restored when coming back to the mirror.
  const ttsSessionRef = useRef(session);
  useEffect(() => {
    if (ttsSessionRef.current === session) return;
    ttsSessionRef.current = session;
    ttsHandleRef.current?.stop("replaced");
  }, [session]);
  // Once a text selection settles inside the body, show the "read from here" pill — assistant
  // turns only.
  const captureTtsSel = () => {
    const sel = window.getSelection();
    const root = bodyRef.current;
    if (!settings.ttsEnabled || !sel || sel.isCollapsed || sel.rangeCount === 0 || !root) {
      setTtsPill(null);
      return;
    }
    const range = sel.getRangeAt(0);
    const node = range.startContainer;
    const el = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
    // Work folded away after completion is out of scope for both auto and footer reading. A pill
    // offered on a selection inside the expanded disclosure would jump to the final answer, which
    // is misleading — so no control is shown for a selection inside it.
    if (el?.closest(".mt-work")) {
      setTtsPill(null);
      return;
    }
    const turnEl = root.contains(node) ? el?.closest<HTMLElement>(".mirror-turn.assistant") : null;
    const turnBody = turnEl ? el?.closest<HTMLElement>(".mirror-turn-body") : null;
    const idx = turnEl?.dataset.turnIdx;
    if (!turnEl || !turnBody || idx === undefined) {
      setTtsPill(null);
      return;
    }
    const block = blockIndexAt(collectBlocks(turnBody), node);
    if (block < 0) {
      setTtsPill(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    setTtsPill({ x: Math.round(rect.left), y: Math.round(rect.top - 34), idx: Number(idx), body: turnBody, block });
  };
  // A touch selection (long press + drag) fires no mouseup, so selectionchange updates it too
  // (lib/selectionCapture) — the same as ReaderView.
  useSelectionCapture(captureTtsSel);
  // Auto read-aloud of a new answer (P2): assistant turns appended by polling go into the reading
  // queue — the active pane only, or every open pane under ttsAutoReadAllPanes, where panes
  // serialise by waiting on the single playback. The first load (tail) and a reset (idx winding
  // back) only re-take the baseline idx; history is never read. Consecutive assistant turns fold
  // into one group that keeps growing, so the queue holds group idxs (no duplicates) and the pump
  // reads only the added blocks. The DOM is already painted by the time this runs (post-commit).
  const syncAutoRead = ({ turns, groups, status }: { turns: Turn[]; groups: Group[]; status: string }) => {
  // Right after a session change this can run on the active change while the PREVIOUS session's
  // turns are still mounted (a swap keeps the instance, replaces only the session prop and makes
  // the drop target active). Building the baseline from those idxs would mis-read the new
  // session's body, so until the session matches we drop the baseline and read nothing; the round
  // that brings the new session's turns re-baselines.
  if (ttsAutoSessionRef.current !== session) {
    ttsAutoSessionRef.current = session;
    ttsAutoSeenRef.current = null;
    return;
  }
  let newest = -1;
  for (let i = turns.length - 1; i >= 0; i--) {
    const x = turns[i].idx;
    if (x !== undefined) {
      newest = x;
      break;
    }
  }
  if (newest < 0) return;
  const seen = ttsAutoSeenRef.current;
  ttsAutoSeenRef.current = newest; // swallow history even in a non-reading pane, so it is never bulk re-read later
  const canRead =
    !readOnly &&
    settings.ttsEnabled &&
    settings.ttsAutoReadMirror &&
    (settings.ttsAutoReadAllPanes ? isTurnReader(session, ttsTokenRef.current) : active);

  if (status === "working" && settings.ttsWorkRead !== "off") {
    // Look only at what follows the current user prompt. The just-sent pending echo counts as a
    // boundary too, so we do not wind back to the previous run's work while the real turn is
    // still landing in history. A queued prompt that has not run yet is not a boundary.
    const lastUser = latestWorkPromptIndex(groups);
    for (let i = lastUser + 1; i < groups.length; i++) {
      const g = groups[i];
      if (g.role !== "assistant" || g.sidechain || g.compact || g.idx === undefined) continue;
      const end = confirmedWorkEnd(g.parts);
      const done = ttsWorkDoneRef.current.get(g.idx) ?? 0;
      if (end <= done) continue;
      ttsWorkDoneRef.current.set(g.idx, end);
      const text = textOfParts(g.parts.slice(done, end));
      if (canRead && seen !== null && text) ttsWorkQueueRef.current.push(text);
    }
    while (ttsWorkQueueRef.current.length > 4) ttsWorkQueueRef.current.shift();
    if (canRead) ttsWorkPumpRef.current();
  } else {
    // idle = the final answer has settled. Stop any remaining quiet reading as a replacement and
    // hand over to the ordinary final-answer reading.
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
  }
  if (!canRead) {
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    return;
  }
  if (seen !== null && newest > seen) {
    const q = ttsAutoQueueRef.current;
    for (const t of turns) {
      if (t.idx === undefined || t.idx <= seen) continue;
      if (t.role !== "assistant" || t.sidechain || t.compact) continue;
      // The group this turn belongs to = the last group whose idx is <= t.idx
      let g: Group | null = null;
      for (const gg of groups) {
        if (gg.idx === undefined) continue;
        if (gg.idx <= t.idx) g = gg;
        else break;
      }
      if (!g || g.idx === undefined || g.role !== "assistant" || g.sidechain || g.compact) continue;
      if (!q.includes(g.idx)) q.push(g.idx);
    }
    while (q.length > 4) q.shift();
  }
  ttsAutoPumpRef.current();
  };

  // Reset on a session switch, called from MirrorView's layout effect in that effect's order.
  const resetForSession = () => {
    ttsAutoSeenRef.current = null; // re-take the auto-read baseline as well (history is not read)
    ttsAutoQueueRef.current.length = 0;
    ttsAutoDoneRef.current.clear();
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
    ttsPendingInitRef.current = false; // re-take the confirmation-reading baseline too
    ttsPendingSigRef.current = "";
  };

  // Reset when the transcript itself was replaced (the server returned a reset). The idxs are
  // renumbered, so the baseline goes too. Not a global stop: the body DOM is being swapped, hence
  // "replaced".
  const resetForTranscript = () => {
    ttsHandleRef.current?.stop("replaced");
    ttsAutoSeenRef.current = null;
    ttsAutoQueueRef.current.length = 0;
    ttsAutoDoneRef.current.clear();
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
  };

  // The pill that reads from the selection. It is portalled outside the body (to document.body)
  // so it sits in viewport coordinates instead of being dragged along by the transcript's scroll.
  const pillPortal =
    ttsPill &&
    createPortal(
      <SelectionFloat x={ttsPill.x} y={ttsPill.y} className="sel-pill-group">
        <button
          type="button"
          className="sel-send-pill"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            ttsStart(ttsPill.idx, ttsPill.body, ttsPill.block);
            setTtsPill(null);
            window.getSelection()?.removeAllRanges();
          }}
        >
          <Icon name="unmute" /> {tr("chat.read_from_here")}
        </button>
      </SelectionFloat>,
      document.body,
    );

  return { wiring: ttsWiring, captureSel: captureTtsSel, pillPortal, syncAutoRead, resetForSession, resetForTranscript };
}
