import { rel } from "../../../core/api/client.ts";
import { getSettings, subscribe as subscribeSettings } from "../../../lib/settings.ts";
import { useLayoutStore } from "../../../layout/store.ts";
import { makeAudioLru } from "../ttsCache.ts";
import { ttsIsBackground, ttsMasterGain, ttsPanePan } from "../ttsControl.ts";
import { type TtsOptions } from "./ttsOptions.ts";
import { voiceCharName } from "./ttsVoices.ts";

// Cap on concurrent synthesis requests, so a long text does not fire dozens at once and
// swamp the engine or CP.
export const MAX_INFLIGHT = 2;
// Gap inserted between chunks, in seconds. CP shortens the leading/trailing silence of the
// material itself (overriding audio_query's pre/postPhonemeLength), so the real interval is
// about this value plus the ~0.07s of silence that remains. Three tiers: after a newline
// (paragraph or line break) a full SENTENCE_GAP beat; after a sentence-ending mark inside a
// line the shorter SENT_BEAT; after an early mid-sentence flush at a comma the tight
// CLAUSE_GAP.
export const SENTENCE_GAP = 0.3;
export const CLAUSE_GAP = 0.08;
// Short beat after a sentence-ending mark within a line. Streaming (startTts) uses it as the
// gap after a chunk; narration (startNarration) takes it from the caller (via turnTts /
// readerText) as the lead-in at a sentence boundary inside the same block or line. Shorter
// than a newline or block head (SENTENCE_GAP / BLOCK_BEAT = 0.3).
export const SENT_BEAT = 0.15;
// Lead-in beat added before the head of a new block: list item, heading, quote. Marker
// characters are not read aloud, so the structural break is conveyed by the pause instead.
// Streaming (startTts) adds it to the normal gap; narration (startNarration) receives it as
// preGaps from the caller (turnTts / ReaderView).
export const BLOCK_BEAT = 0.3;
// Lead-in for a line that opens with a suspension mark (dash, ellipsis; see startsTame). It
// dramatises "pause, then speak", so it is longer than a normal block head (BLOCK_BEAT) and
// long enough that the gap is clearly felt (measured on a real device).
export const TAME_BEAT = 0.6;
// Fragments shorter than this are merged with the next sentence before being read, to avoid
// choppy playback. A newline or sentence end forces a flush regardless.
export const MIN_CHUNK = 6;

// Characters treated as a sentence end or break; everything up to one of them settles as a
// chunk.
export const SENTENCE_END = /[。．！？!?\n]/;

// Controller for reading one chat turn: start it when send() begins, push on onDelta, flush
// on onDone, abort with stop().
// --- Synthesis cache ------------------------------------------------------------
// An in-memory LRU of decoded AudioBuffers keyed by text plus synthesis conditions, so a
// repeat reading (pressing read-aloud on the same answer again, a stock announce, restarting
// a narration) plays instantly with no synthesis and no network. Reusing an AudioBuffer is
// safe because each playback builds a fresh AudioBufferSourceNode. The bound is total
// playback seconds (setting ttsCacheSec, 0 = disabled); VOICEVOX 24kHz mono float32 PCM runs
// about 0.1MB per second. Not persisted, so a reload clears it.

const synthCache = makeAudioLru<AudioBuffer>(() => getSettings().ttsCacheSec);

// Buffer -> the provider that actually synthesised it (the response's X-TTS-Provider). CP
// decides where auto goes, based on engine reachability, language and admin toggles, so
// claiming "this is Zundamon" from the setting alone would be a lie: a deployment without
// VOICEVOX routes Japanese to Polly too. A WeakMap, so entries evicted from the LRU are
// collected with their buffers.
const bufProvider = new WeakMap<AudioBuffer, string>();

// heardProvider returns the provider that actually produced a buffer, or "" when unknown
// (an older CP, or a buffer from outside the cache).
export function heardProvider(ab: AudioBuffer | null | undefined): string {
  return (ab && bufProvider.get(ab)) || "";
}

// The key is the synthesis conditions plus the text, separated by NUL, which cannot occur in
// the text. provider is stored as the configured value (including auto), so right after
// auto's routing changes the old engine's voice may still play; eviction resolves it, and
// that is accepted.
function synthCacheKey(text: string, opts: TtsOptions): string {
  return [opts.provider, opts.voice, opts.speed, opts.enkana ? 1 : 0, opts.pollyVoice ?? "", opts.lang ?? "", opts.particlePause ? 1 : 0, text].join(
    "\u0000",
  );
}

// synthToBuffer synthesises one sentence through CP's /api/tts/synthesize and decodes it to
// an AudioBuffer, returning a cache hit immediately. Failure (abort, network, non-200, decode
// error) returns null and the caller skips that sentence. Shared by streaming playback
// (startTts) and narration (startNarration).
export async function synthToBuffer(
  ctx: AudioContext,
  text: string,
  opts: TtsOptions,
  signal: AbortSignal,
): Promise<AudioBuffer | null> {
  const key = synthCacheKey(text, opts);
  const hit = synthCache.get(key);
  if (hit) return hit;
  try {
    const res = await fetch(rel("api/tts/synthesize"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        text,
        provider: opts.provider,
        voice: opts.voice,
        speed: opts.speed,
        enkana: opts.enkana ?? false,
        pollyVoice: opts.pollyVoice ?? "",
        lang: opts.lang ?? "",
        particlePause: opts.particlePause ?? false,
      }),
      signal,
    });
    if (!res.ok) return null;
    const arr = await res.arrayBuffer();
    const ab = await ctx.decodeAudioData(arr);
    // The provider that actually produced this audio. CP decides where auto goes (engine
    // reachability, language, admin toggles), so the setting alone cannot tell. Remembering it
    // per buffer carries the same answer into playback that hits the cache.
    const actual = res.headers.get("X-TTS-Provider");
    if (actual) bufProvider.set(ab, actual);
    synthCache.put(key, ab);
    return ab;
  } catch {
    return null;
  }
}

// One shared AudioContext and one master gain for the final output. Each playback's own
// volume (the quiet work-trace reading, say) is applied first; the master then moves the
// background volume for every playback at once, smoothly.
let sharedCtx: AudioContext | null = null;
let masterGain: GainNode | null = null;
let backgroundEventsWired = false;
function masterTarget(): number {
  const hidden = typeof document !== "undefined" && document.hidden;
  const focused = typeof document === "undefined" || document.hasFocus();
  return ttsMasterGain(getSettings().ttsBackgroundPlayback, ttsIsBackground(hidden, focused), getSettings().ttsBackgroundVolume);
}

// voiceLoudness is the per-voice output multiplier. Zundamon is naturally louder than the
// other characters, so the ttsZundamonVolume setting trims it to match the other voices and
// the notification sounds (measured on a real device); Polly and every other character get 1.
// It consults heard for the same reason the label does: applying Zundamon's attenuation while
// auto has fallen back to Polly would quieten a voice that is not the one playing.
function voiceLoudness(opts: TtsOptions, heard = ""): number {
  if (voiceCharName(opts, heard) === "ずんだもん") return Math.max(0, Math.min(1, getSettings().ttsZundamonVolume));
  return 1;
}

// outputVolume is the playback gain to wire in: the caller's volume (the quiet work-trace
// reading, say) times the per-voice multiplier. Shared by all three connectOutput call sites
// (streaming, narration, announcement).
export function outputVolume(opts: TtsOptions, heard = ""): number {
  return (opts.volume ?? 1) * voiceLoudness(opts, heard);
}

function syncMasterGain(immediate = false): void {
  if (!sharedCtx || !masterGain) return;
  const gain = masterGain.gain;
  const now = sharedCtx.currentTime;
  const target = masterTarget();
  gain.cancelScheduledValues(now);
  if (immediate) {
    gain.setValueAtTime(target, now);
    return;
  }
  // Reaches 95% of the target in about 150ms, avoiding the click an instant change makes.
  gain.setValueAtTime(gain.value, now);
  gain.setTargetAtTime(target, now, 0.05);
}

export function audioCtx(): AudioContext | null {
  try {
    if (!sharedCtx) {
      sharedCtx = new (window.AudioContext || (window as any).webkitAudioContext)();
      masterGain = sharedCtx.createGain();
      masterGain.connect(sharedCtx.destination);
      syncMasterGain(true);
    }
    if (!backgroundEventsWired && typeof document !== "undefined" && typeof window !== "undefined") {
      backgroundEventsWired = true;
      document.addEventListener("visibilitychange", () => syncMasterGain());
      window.addEventListener("blur", () => syncMasterGain());
      window.addEventListener("focus", () => syncMasterGain());
    }
    if (sharedCtx.state === "suspended") void sharedCtx.resume();
    return sharedCtx;
  } catch {
    return null;
  }
}

export function connectOutput(ctx: AudioContext, src: AudioBufferSourceNode, volume = 1, paneId?: string): void {
  const destination: AudioNode = masterGain ?? ctx.destination;
  let output: AudioNode = src;
  if (volume < 0.999) {
    const gain = ctx.createGain();
    gain.gain.value = Math.max(0, Math.min(1, volume));
    output.connect(gain);
    output = gain;
  }
  const s = getSettings();
  const pan = ttsPanePan(s.ttsStereoByPane, useLayoutStore.getState().layout, paneId);
  if (Math.abs(pan) > 0.001 && typeof ctx.createStereoPanner === "function") {
    const panner = ctx.createStereoPanner();
    panner.pan.value = pan;
    output.connect(panner);
    output = panner;
  }
  output.connect(destination);
}

// Apply a settings change to audio already playing, the moment it is toggled. A settings
// update arriving from server sync goes through the same subscription, so neither waits for
// the next visibilitychange or focus.
subscribeSettings(() => syncMasterGain());
