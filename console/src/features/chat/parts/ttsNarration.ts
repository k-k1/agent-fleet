import { getSettings } from "../../../lib/settings.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import { plainify, splitLongSentence } from "../ttsText.ts";
import { effectiveDict } from "../ttsDict.ts";
import { type TtsController, type TtsEndReason, type TtsStopReason } from "../ttsControl.ts";
import { type TtsOptions, ttsOptsFromSettings, localizedReadings } from "./ttsOptions.ts";
import { emotionOpts, voiceCharName } from "./ttsVoices.ts";
import { MAX_INFLIGHT, audioCtx, connectOutput, heardProvider, outputVolume, synthToBuffer } from "./ttsAudio.ts";
import { notifyStopped, preemptActive, takeAnnounce } from "./ttsPlay.ts";

// --- Narration mode (docs/log/24): read a file's body from the top, with karaoke follow ---
// units (the plain text of each block) are synthesised and played in order, and the index of
// the unit that starts playing is reported through onUnit, which the caller (FileView) uses to
// highlight and scroll that element. It reuses startTts's synthesis, sequential playback and
// single-global-playback machinery, but adds pause/resume (AudioContext suspend/resume) and
// per-unit progress reporting.

export interface NarrationHandle {
  pause(): void;
  resume(): void;
  stop(reason?: TtsStopReason): void;
  isPaused(): boolean;
  // Immediate voice switch (the narration view's select). The sentence currently playing is
  // left alone; the new voice starts with the next one.
  setVoice(voice?: Partial<TtsOptions>): void;
}

export function startNarration(
  units: string[],
  source: string,
  // onUnit(i) = unit i started playing. onUnit(null, reason) = finished: done (natural end),
  // explicit (stopped on purpose), replaced (another playback took over). The mirror's
  // auto-read queue uses the reason to decide whether to continue.
  onUnit: (i: number | null, endReason?: TtsEndReason) => void,
  // Voice override (per-session sessionVoiceOpts and the like). Unset means the speaker from
  // settings.
  voice?: Partial<TtsOptions>,
  // Per-unit lead-in beat, in seconds. Passing BLOCK_BEAT for the first sentence of a new
  // block (list item, paragraph head) inserts a beat before reading it. The first unit's beat
  // is ignored, since it would only delay the start.
  preGaps?: number[],
  // Originating session name, for the left pane's playing icon. Empty for non-session
  // narration.
  sessionName = "",
): NarrationHandle {
  preemptActive(); // one global playback: stop what is playing, keep the queue
  const ctx = audioCtx();
  const s = getSettings();
  let opts = { ...ttsOptsFromSettings(s), ...voice }; // let: setVoice replaces it
  const userDict = effectiveDict(); // user plus tenant-wide dictionary (user wins)

  // Clean each unit up for reading: strip markdown syntax and URLs, abbreviate code
  // fragments, apply the user dictionary. A unit that ends up empty (pure code, say) stays
  // "" so the original indices are preserved; playback and highlighting skip it.
  const codeOpts = { abbrev: s.ttsAbbrevCode, dict: userDict };
  const texts = units.map((u) => {
    let t = plainify(u, codeOpts).trim();
    if (t) t = localizedReadings(t, userDict, s.ttsParticlePause); // dictionary -> reading fixups -> particle pauses
    return t;
  });

  const buffers = new Map<number, AudioBuffer | null>(); // index -> decoded buffer (null = empty or failed)
  const acs = new Set<AbortController>();
  let synthAt = 0; // index to synthesise next
  let cursor = 0; // index to play next
  let epoch = 0; // voice generation, bumped by setVoice to invalidate older syntheses
  // The provider that actually synthesised (X-TTS-Provider). CP decides where auto goes, so
  // this is unknown until the first unit comes back; restate TopBar's voice label then.
  let heard = "";
  const noteHeard = (ab: AudioBuffer) => {
    const h = heardProvider(ab);
    if (!h || h === heard) return;
    heard = h;
    const st = useTtsStore.getState();
    if (st.active === adapter) st.setActive(adapter, source, voiceCharName(opts, heard), sessionName);
  };
  let inflight = 0;
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
  let paused = false;
  let stopped = false;
  let ended = false;

  const finish = (reason: TtsEndReason) => {
    if (ended) return;
    ended = true;
    onUnit(null, reason);
    const st = useTtsStore.getState();
    if (st.active === adapter) {
      st.setActive(null, "");
      st.setSpeaking(false);
      st.setPreparing(false);
    }
  };

  const maybeDone = () => {
    if (!stopped && !playing && inflight === 0 && cursor >= texts.length) finish("done");
  };

  // Synthesise ahead up to the in-flight limit; an empty unit becomes null at once. A result
  // is accepted only when its epoch still matches, so a stale voice that arrives after
  // setVoice is never played.
  const pump = () => {
    while (!stopped && inflight < MAX_INFLIGHT && synthAt < texts.length) {
      const i = synthAt++;
      const text = texts[i];
      if (!text) {
        buffers.set(i, null);
        continue;
      }
      inflight++;
      const ac = new AbortController();
      acs.add(ac);
      const ep = epoch;
      synthToBuffer(ctx!, text, emotionOpts(text, opts), ac.signal)
        .then((ab) => {
          if (ep === epoch) buffers.set(i, ab);
        })
        .catch(() => {
          if (ep === epoch) buffers.set(i, null);
        })
        .finally(() => {
          acs.delete(ac);
          inflight--;
          pump();
          tryPlay();
        });
    }
  };

  // Immediate voice switch (NarrationHandle.setVoice): leave the sentence playing alone and
  // start the new voice at the next one. Drop the unplayed read-ahead (buffers deletes played
  // entries as it advances, so everything left is at or after cursor) and resynthesise from
  // cursor. In-flight requests are aborted, and the epoch reliably invalidates any that race
  // back out of order.
  const setVoice = (voice2?: Partial<TtsOptions>) => {
    if (stopped) return;
    opts = { ...ttsOptsFromSettings(getSettings()), ...voice2 };
    epoch++;
    acs.forEach((a) => a.abort());
    acs.clear();
    buffers.clear();
    synthAt = cursor;
    heard = ""; // a new voice may route elsewhere; restate from the next unit's response
    const st = useTtsStore.getState();
    if (st.active === adapter) st.setActive(adapter, source, voiceCharName(opts), sessionName); // refresh TopBar's voice label
    pump();
    tryPlay(); // in case playback had caught up and was waiting
  };

  // Interleaved announcements (docs/log/24): session notifications and confirmations must not
  // wait out a long narration. At a unit boundary, take one item off the announce queue and
  // read it before the next unit. While it plays, TopBar's label and voice swap to the
  // announcement's and swap back afterwards. Stop and pause act on both together (same ctx,
  // same adapter).
  const playInterlude = (a: { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }) => {
    let t = plainify(a.text, codeOpts).trim();
    if (t) t = localizedReadings(t, userDict, getSettings().ttsParticlePause);
    if (!t) {
      tryPlay();
      return;
    }
    const aopts = { ...ttsOptsFromSettings(getSettings()), ...a.voice };
    let aheard = ""; // where the announcement actually synthesised, kept apart from narration's heard
    const label = (on: boolean) => {
      const st = useTtsStore.getState();
      if (st.active !== adapter) return;
      if (on) st.setActive(adapter, a.source, voiceCharName(aopts, aheard), a.sessionName ?? "", a.purpose ?? "reading");
      else st.setActive(adapter, source, voiceCharName(opts, heard), sessionName); // back to the narration's label
    };
    // Split a long announcement (a summary, say) for synthesis and play the pieces in order:
    // one heavy synthesis call would be a silent wait.
    const pieces = splitLongSentence(t);
    let pi = 0;
    playing = true; // hold off the next unit and maybeDone while synthesis is pending
    const playNext = () => {
      if (stopped) return;
      if (pi >= pieces.length) {
        playing = false;
        label(false);
        tryPlay();
        maybeDone();
        return;
      }
      const piece = pieces[pi++];
      const ac = new AbortController();
      acs.add(ac);
      void synthToBuffer(ctx!, piece, emotionOpts(piece, aopts), ac.signal).then((ab) => {
        acs.delete(ac);
        if (stopped) {
          playing = false;
          return;
        }
        if (!ab) {
          playNext(); // skip a failed piece
          return;
        }
        aheard = heardProvider(ab) || aheard;
        label(true);
        const src = ctx!.createBufferSource();
        src.buffer = ab;
        connectOutput(ctx!, src, outputVolume(aopts, aheard), aopts.paneId);
        src.onended = () => {
          cur = null;
          playNext();
        };
        cur = src;
        src.start(ctx!.currentTime + (pi === 1 ? 0.15 : 0)); // small gap from the narration, first piece only
        useTtsStore.getState().setSpeaking(true);
        useTtsStore.getState().setPreparing(false);
      });
    };
    playNext();
  };

  // Play in index order, skipping empty and failed units, and report each newly started unit
  // through onUnit.
  const tryPlay = () => {
    if (stopped || paused || playing || !ctx) return;
    if (cursor < texts.length) {
      const a = takeAnnounce(); // interleave only while narration continues; the tail runs plainly
      if (a) {
        playInterlude(a);
        return;
      }
    }
    while (cursor < texts.length && buffers.has(cursor)) {
      const ab = buffers.get(cursor)!;
      buffers.delete(cursor);
      const idx = cursor;
      cursor++;
      if (!ab) continue; // empty or failed: move on without highlighting
      playing = true;
      onUnit(idx);
      useTtsStore.getState().setSpeaking(true);
      useTtsStore.getState().setPreparing(false); // sound has started: clear "generating"
      noteHeard(ab); // the real synthesis target is known now; fix TopBar's voice label (auto's fallback)
      const src = ctx.createBufferSource();
      src.buffer = ab;
      connectOutput(ctx, src, outputVolume(opts, heard), opts.paneId);
      src.onended = () => {
        playing = false;
        cur = null;
        if (stopped) return;
        tryPlay();
        maybeDone();
      };
      cur = src;
      // Lead-in beat at the head of a block. The highlight (onUnit) lands first, but the gap
      // reads as "next is here", which suits karaoke.
      const pre = idx > 0 ? (preGaps?.[idx] ?? 0) : 0;
      src.start(ctx.currentTime + pre);
      return;
    }
    maybeDone();
  };

  const adapter: TtsController = { push() {}, flush() {}, stop: (reason) => stop(reason) };

  const pause = () => {
    if (stopped || paused) return;
    paused = true;
    if (ctx && ctx.state === "running") void ctx.suspend(); // stops the current sound too
  };
  const resume = () => {
    if (stopped || !paused) return;
    paused = false;
    if (ctx) void ctx.resume();
    tryPlay(); // nudge playback in case the pause landed on a sentence boundary
  };
  const stop = (reason: TtsStopReason = "explicit") => {
    if (stopped || ended) return;
    stopped = true;
    acs.forEach((a) => a.abort());
    acs.clear();
    if (cur) {
      try {
        cur.stop();
      } catch {}
      cur = null;
    }
    playing = false;
    if (ctx && ctx.state === "suspended") void ctx.resume(); // restore it for the next playback
    finish(reason);
    if (reason === "explicit") notifyStopped(); // only a user stop also discards the waiting queue
  };

  useTtsStore.getState().setActive(adapter, source, voiceCharName(opts), sessionName);
  // Defer the first kick to a microtask so onUnit runs only after the caller (FileView) has
  // settled the returned handle and its own state. Otherwise, with no content or no engine,
  // finish fires synchronously and onUnit(null) arrives before beginNarration finishes its
  // setup, leaving the state inconsistent.
  queueMicrotask(() => {
    if (stopped) return;
    if (!ctx || texts.every((t) => !t)) {
      finish("done"); // no engine (AudioContext unavailable) or nothing to read
      return;
    }
    useTtsStore.getState().setPreparing(true); // "generating" (TopBar spinner) until the first sound
    pump();
    tryPlay();
  });
  return { pause, resume, stop, isPaused: () => paused, setVoice };
}
