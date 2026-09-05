import { getSettings } from "../../../lib/settings.ts";
import { t } from "../../../lib/i18n/index.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import { plainifyStreaming, firstChunkCut, splitLongSentence, startsBlock, startsTame } from "../ttsText.ts";
import { effectiveDict } from "../ttsDict.ts";
import { type TtsController, type TtsEndReason } from "../ttsControl.ts";
import { type TtsOptions, ttsOptsFromSettings, localizedReadings } from "./ttsOptions.ts";
import { emotionOpts, voiceCharName } from "./ttsVoices.ts";
import { BLOCK_BEAT, CLAUSE_GAP, MAX_INFLIGHT, MIN_CHUNK, SENTENCE_END, SENTENCE_GAP, SENT_BEAT, TAME_BEAT, audioCtx, connectOutput, heardProvider, outputVolume, synthToBuffer } from "./ttsAudio.ts";


// --- Global stop propagation -----------------------------------------------------
// There are two kinds of stop. (1) An explicit stop (TopBar / turn-footer stop button) means
// "be quiet", so the pending announce queue and every mirror pane's auto-read queue are
// discarded too. (2) A replacement caused by a new playback starting (preemption, keeping a
// single global playback) is not a stop, so nothing is discarded. stop(reason) states which,
// so that asynchronous and re-entrant playback never depends on a transient global flag.
export function preemptActive(): void {
  const st = useTtsStore.getState();
  if (!st.active) return;
  st.active.stop("replaced");
}
// onTtsStop subscribes to explicit stops (so mirrors can drop their auto-read queue). Returns
// the unsubscribe function.
const stopSubs = new Set<() => void>();
export function onTtsStop(fn: () => void): () => void {
  stopSubs.add(fn);
  return () => {
    stopSubs.delete(fn);
  };
}
export function notifyStopped(): void {
  announceQueue.length = 0;
  stopSubs.forEach((f) => f());
}

// startTts begins one read-aloud session. The whole app plays at most one at a time, so an
// existing playing session is stopped first and this one registers itself as active in the
// global store. source is the label shown in the TopBar "now reading" indicator. onEnd is
// called exactly once for natural completion ("done") and for a stop ("explicit" / "replaced");
// the announce queue relies on that to run serially.
export function startTts(
  opts: TtsOptions,
  source = "",
  onEnd?: (reason: TtsEndReason) => void,
  sessionName = "", // originating session name (left pane playing icon; "" when not a session)
  // onPiece(spoken): fires at the moment this sentence actually starts sounding, carrying the
  // display text before reading corrections (live karaoke, docs/log/19). Costs nothing if unset.
  onPiece?: (spoken: string) => void,
  purpose: "reading" | "session-notification" | "usage-notification" | "manual" = "reading",
): TtsController {
  preemptActive(); // stop the previous playback (one global playback; queues are kept)
  // The reading dictionary (user + tenant merged, user wins) is built once per turn. It is text
  // processing independent of opts.provider/voice, so it is deliberately not part of opts.
  const userDict = effectiveDict();
  const codeOpts = { abbrev: getSettings().ttsAbbrevCode, dict: userDict }; // abbreviated reading of inline code
  const ctx = audioCtx();
  let buf = ""; // unterminated buffer (middle of a sentence)
  let pending = ""; // short fragment carried over because it is below MIN_CHUNK
  let pendingPre = 0; // pre-beat of the carried fragment (s; block head=BLOCK_BEAT / pause=TAME_BEAT / none=0)
  let inFence = false; // inside ```code``` (skipped when reading)
  let seq = 0; // sequence number of submitted sentences
  let inflight = 0;
  const jobs: { seq: number; text: string }[] = [];
  const buffers = new Map<number, AudioBuffer | null>(); // seq -> decoded (null = failed/skipped)
  const gaps = new Map<number, number>(); // seq -> gap inserted after that chunk (s)
  const preGaps = new Map<number, number>(); // seq -> pre-beat added before that chunk (block head)
  const displays = new Map<number, string>(); // seq (sentence-head piece) -> display text before reading corrections (for onPiece)
  const pieceTimers = new Set<number>(); // scheduled onPiece firings (cleared on stop)
  let playCursor = 0; // next seq to sound
  // The provider that actually synthesised (X-TTS-Provider). With the setting on auto the CP
  // decides the destination, so it is unknown until the first sentence comes back; once known,
  // the TopBar voice label is corrected.
  let heard = "";
  const noteHeard = (ab: AudioBuffer) => {
    const h = heardProvider(ab);
    if (!h || h === heard) return;
    heard = h;
    const st = useTtsStore.getState();
    if (st.active === controller) st.setActive(controller, source, voiceCharName(opts, heard), sessionName, purpose);
  };
  const srcs = new Set<AudioBufferSourceNode>(); // nodes playing plus those scheduled ahead
  let nextStartAt = 0; // AudioContext time at which the next buffer starts
  let stopped = false;
  let startedAudio = false; // true once the first sentence is submitted (= reading has begun)
  let flushed = false; // stream complete (no more sentences will arrive)
  let ended = false; // guard so onEnd is called exactly once
  const acs = new Set<AbortController>();

  const finish = (reason: TtsEndReason) => {
    if (ended) return;
    ended = true;
    // Deregister if still active, natural completion included: a leftover registration is
    // indistinguishable from "a playback is being prepared", and the waiting pumps (announce,
    // mirror auto-read) would then wait forever.
    const st = useTtsStore.getState();
    if (st.active === controller) {
      st.setActive(null, "");
      st.setSpeaking(false);
      st.setPreparing(false);
    }
    onEnd?.(reason);
  };

  // Report to the store whether playback is live (awaiting synthesis / sounding / stream still
  // open). To avoid flicker in the momentary gap between sentences, it stays true from
  // startedAudio until flush; once flushed and empty, report natural completion (done).
  const notify = () => {
    if (stopped) return;
    const active = startedAudio && (srcs.size > 0 || jobs.length > 0 || inflight > 0 || !flushed);
    useTtsStore.getState().setSpeaking(active);
    if (flushed && !active) finish("done");
  };

  // Cut complete sentences out of the buffer, plainify them and submit them as jobs.
  const drain = (force: boolean) => {
    // For the very first utterance only, emit a short head of the first sentence early (cut at a
    // comma or by length) so playback starts sooner: synthesising the whole first sentence turns
    // its synthesis time into start latency. speakText/announce push in one go through the same
    // path. The cut is mid-sentence, so the following gap is tightened; after startedAudio the
    // granularity is a full sentence.
    if (!startedAudio) {
      const m = buf.match(SENTENCE_END);
      const head = m ? buf.slice(0, m.index! + 1) : buf;
      const cut = firstChunkCut(head);
      if (cut > 0) {
        enqueuePiece(buf.slice(0, cut), /*hard*/ true, CLAUSE_GAP);
        buf = buf.slice(cut);
      }
    }
    for (;;) {
      const m = buf.match(SENTENCE_END);
      if (!m) break;
      const end = m.index! + 1;
      const piece = buf.slice(0, end);
      buf = buf.slice(end);
      const nl = /\n/.test(m[0]);
      // Terminated by a newline -> a full beat (SENTENCE_GAP); a mid-text full stop -> a short
      // beat (SENT_BEAT).
      enqueuePiece(piece, /*hard*/ nl || /[。．！？!?]/.test(m[0]), nl ? SENTENCE_GAP : SENT_BEAT);
    }
    if (force) {
      // Read out the unterminated tail plus anything carried over. The fence state is carried on.
      const tailPre = !pending ? preBeatOf(buf) : 0; // with nothing carried, judge on the tail's head
      const spokenTail = plainifyStreaming(buf, { get: () => inFence, set: (v) => (inFence = v) }, codeOpts);
      buf = "";
      const combined = (pending + spokenTail).trim();
      const pre = pending ? pendingPre : tailPre;
      pending = "";
      pendingPre = 0;
      if (combined) submit(combined, SENTENCE_GAP, pre);
    }
  };

  // Pre-beat (seconds) judged from the head of a chunk's opening fragment: BLOCK_BEAT for a block
  // head (list, heading, quote), TAME_BEAT for a dramatic pause (-- , ... and similar), else 0.
  const preBeatOf = (s: string): number => (startsBlock(s) ? BLOCK_BEAT : startsTame(s) ? TAME_BEAT : 0);

  const enqueuePiece = (piece: string, hard: boolean, gap = SENTENCE_GAP) => {
    const pre = pending ? pendingPre : preBeatOf(piece);
    const spoken = plainifyStreaming(
      piece,
      {
        get: () => inFence,
        set: (v) => (inFence = v),
      },
      codeOpts,
    );
    if (!spoken.trim()) {
      // A fragment with nothing to read, only a newline (a paragraph break): promote the previous
      // chunk's trailing gap to a paragraph gap. A sentence ending in a full stop already got
      // SENT_BEAT, so only the end of a paragraph gains the full beat here. If the previous chunk
      // is already scheduled (its gap consumed) it is too late, so leave it alone.
      if (/\n/.test(piece) && gaps.has(seq - 1)) gaps.set(seq - 1, Math.max(gaps.get(seq - 1)!, SENTENCE_GAP));
      return;
    }
    const combined = pending + spoken;
    if (!hard && combined.trim().length < MIN_CHUNK) {
      pending = combined; // still short -> merge with the next one
      pendingPre = pre;
      return;
    }
    pending = "";
    pendingPre = 0;
    submit(combined, gap, pre);
  };

  const submit = (text: string, gap = SENTENCE_GAP, pre = 0) => {
    let t = text.trim();
    if (!t) return;
    const display = t; // karaoke display text (before reading corrections and splitting; sent via onPiece)
    // Shape the reading: user/tenant dictionary -> built-in reading corrections -> particle
    // micro-pauses (enkana runs later, on the CP side).
    t = localizedReadings(t, userDict, getSettings().ttsParticlePause);
    if (!t) return;
    // Split a long sentence for synthesis: one heavy synthesis starves the read-ahead and leaves
    // silence. Intermediate pieces get a comma-sized gap, the real gap goes only after the last
    // piece, and the pre-beat (block head) applies only to the first.
    const pieces = splitLongSentence(t);
    for (let i = 0; i < pieces.length; i++) {
      gaps.set(seq, i === pieces.length - 1 ? gap : CLAUSE_GAP);
      if (pre && i === 0) preGaps.set(seq, pre); // head of a list/heading or a pause -> beat before reading
      if (onPiece && i === 0) displays.set(seq, display); // report this sentence when its head piece sounds
      jobs.push({ seq: seq++, text: pieces[i] });
    }
    if (!startedAudio) useTtsStore.getState().setPreparing(true); // "generating" until the first sound
    startedAudio = true;
    pump();
    notify();
  };

  // Keep synthesis running, filling in-flight requests up to the limit.
  const pump = () => {
    while (!stopped && inflight < MAX_INFLIGHT && jobs.length) {
      const job = jobs.shift()!;
      inflight++;
      synth(job.text)
        .then((ab) => buffers.set(job.seq, ab))
        .catch(() => buffers.set(job.seq, null))
        .finally(() => {
          inflight--;
          tryPlay();
          pump();
          notify();
        });
    }
  };

  const synth = async (text: string): Promise<AudioBuffer | null> => {
    if (!ctx) return null;
    const ac = new AbortController();
    acs.add(ac);
    try {
      return await synthToBuffer(ctx, text, emotionOpts(text, opts), ac.signal);
    } finally {
      acs.delete(ac);
    }
  };

  // Play in sequence order, waiting when the next seq has not arrived, so order survives
  // out-of-order synthesis. Calling start() only after onended would insert an event-loop gap
  // every time, so a ready buffer is scheduled ahead on the AudioContext clock at
  // "previous end time + SENTENCE_GAP". If playback had caught up (that time is in the past) it
  // starts immediately.
  const tryPlay = () => {
    if (stopped || !ctx) return;
    while (buffers.has(playCursor)) {
      const sq = playCursor;
      const ab = buffers.get(sq)!;
      buffers.delete(sq);
      const gap = gaps.get(sq) ?? SENTENCE_GAP;
      gaps.delete(sq);
      const pre = preGaps.get(sq) ?? 0;
      preGaps.delete(sq);
      playCursor++;
      if (!ab) continue; // skip a failed sentence and move on
      noteHeard(ab); // the real synthesis target is now known; fix the TopBar voice label (auto fallback)
      const src = ctx.createBufferSource();
      src.buffer = ab;
      connectOutput(ctx, src, outputVolume(opts, heard), opts.paneId);
      src.onended = () => {
        srcs.delete(src);
        notify();
      };
      srcs.add(src);
      let at = Math.max(ctx.currentTime, nextStartAt);
      if (sq > 0) at += pre; // block-head pre-beat (never delay the very first chunk)
      src.start(at);
      // Live karaoke: fire onPiece at the time this sentence actually starts sounding, which is
      // ahead of now because buffers are scheduled in batches by the read-ahead - so align with
      // the playback time, not with the moment start() is called.
      if (onPiece) {
        const d = displays.get(sq);
        displays.delete(sq);
        if (d) {
          const delayMs = Math.max(0, (at - ctx.currentTime) * 1000);
          const tm = window.setTimeout(() => {
            pieceTimers.delete(tm);
            if (!stopped) onPiece(d);
          }, delayMs);
          pieceTimers.add(tm);
        }
      }
      useTtsStore.getState().setPreparing(false); // first sound scheduled -> clear "generating"
      nextStartAt = at + ab.duration + gap;
    }
  };

  const controller: TtsController = {
    push(delta: string) {
      if (stopped) return;
      buf += delta;
      drain(false);
    },
    flush() {
      if (stopped) return;
      flushed = true;
      drain(true);
      notify();
    },
    stop(reason = "explicit") {
      // Natural completion leaves callers holding a harmless stale controller. A later
      // lifecycle cleanup must not turn that old handle into a new global stop event.
      if (stopped || ended) return;
      stopped = true;
      jobs.length = 0;
      pieceTimers.forEach((t) => clearTimeout(t)); // cancel scheduled onPiece firings
      pieceTimers.clear();
      acs.forEach((a) => a.abort());
      acs.clear();
      srcs.forEach((s) => {
        try {
          s.stop(); // discard both sounding and already-scheduled (not yet started) nodes
        } catch {}
      });
      srcs.clear();
      finish(reason); // finish does the store cleanup (clearing active/speaking)
      if (reason === "explicit") notifyStopped(); // only a user stop also discards the waiting queues
    },
  };
  useTtsStore.getState().setActive(controller, source, voiceCharName(opts), sessionName, purpose);
  return controller;
}

// --- Serial announce queue (docs/log/24 Tier1: background session notifications, etc.) --------
// Reads short announcements one at a time and never interrupts: if anything is playing (a chat
// reading, say) it waits for that to end. When more than 4 pile up the oldest are dropped, so a
// flood does not greet someone returning to their desk.
const announceQueue: { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }[] = [];
let announcing = false;

// voice overrides the voice, e.g. the per-session one (sessionVoiceOpts); unset uses the speaker
// from settings. sessionName is the originating session (left pane playing icon); omit it for an
// announcement that does not belong to a session.
export function announce(text: string, source = "", voice?: Partial<TtsOptions>, sessionName = "", purpose: "reading" | "session-notification" | "usage-notification" | "manual" = "reading"): void {
  const t = text.trim();
  if (!t) return;
  announceQueue.push({ text: t, source, voice, sessionName, purpose });
  while (announceQueue.length > 4) announceQueue.shift();
  pumpAnnounce();
}

function pumpAnnounce(): void {
  if (announcing) return;
  // Something playing or preparing (registered but no sound yet) -> resume after it ends (see the
  // subscribe below). Checking speaking alone would interrupt a playback still awaiting
  // synthesis, so active is checked too.
  const st = useTtsStore.getState();
  if (st.speaking || st.active) return;
  const next = announceQueue.shift();
  if (!next) return;
  announcing = true;
  const c = startTts(
    { ...ttsOptsFromSettings(), ...next.voice },
    next.source,
    (reason) => {
      announcing = false;
      if (reason === "explicit") announceQueue.length = 0; // a global stop discards the queue too
      else pumpAnnounce();
    },
    next.sessionName ?? "",
    undefined,
    next.purpose ?? "reading",
  );
  c.push(next.text);
  c.flush();
}

// When a playback (a chat reading, say) ends elsewhere, resume the announcements that were made
// to wait. zustand's subscribe runs synchronously inside setState, and during a preemption (stop
// the old playback -> register the new one) active is momentarily null, so defer to a microtask
// and judge on the state after the replacement is complete.
useTtsStore.subscribe((st, prev) => {
  if (prev.speaking && !st.speaking) queueMicrotask(pumpAnnounce);
});

// takeAnnounce lets a narration (startNarration) pull an announcement in at a unit boundary, so a
// long narration (a file, a long turn) does not hold session notifications back (docs/log/24).
// pumpAnnounce does not run while something is playing (active set), so this is the only place
// that dequeues during playback - nothing can be played twice.
export function takeAnnounce():
  | { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }
  | undefined {
  return announceQueue.shift();
}

// speakText reads the given text immediately (non-streaming uses such as a FileView selection).
// Settings (speaker/speed/provider) come from getSettings() because this runs outside React.
// voice overrides the voice (the assistant voice assistantVoiceOpts, for instance). Empty text
// is ignored.
export function speakText(text: string, source = "", voice?: Partial<TtsOptions>): void {
  const t = text.trim();
  if (!t) return;
  const c = startTts({ ...ttsOptsFromSettings(), ...voice }, source, undefined, "", undefined, "manual");
  c.push(t);
  c.flush();
}

// previewVoice auditions a character from the settings list by reading a short fixed phrase. It
// goes through the single global playback, so the TopBar stop applies; the same phrase under the
// same conditions is served from the synthesis cache and plays instantly after the first time.
export function previewVoice(name: string, voice: string, speed?: number): void {
  const opts = { ...ttsOptsFromSettings(), provider: "auto", voice };
  if (speed) opts.speed = speed;
  const c = startTts(opts, t("tts.preview_label", { name }));
  // The audition phrase is an audio sample read by a Japanese-speaking character, so it stays
  // Japanese regardless of the UI locale.
  c.push("こんにちは。この声で読み上げます。");
  c.flush();
}
