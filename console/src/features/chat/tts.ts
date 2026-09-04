// features/chat/tts - read-aloud for agent answers (docs/log/24 + ADR0013).
//
// Takes the deltas of a stream, cuts out only the sentences a full stop has settled, and posts them
// one by one to the CP's /api/tts/synthesize (VOICEVOX / Zundamon). Synthesis limits how many
// requests are in flight (backpressure) and playback follows the sentence sequence, not arrival
// order. A ready buffer is scheduled ahead of time on the AudioContext clock at "previous end +
// SENTENCE_GAP", which pins the gap between sentences to its designed value; an onended-driven
// start() would add a fresh event-loop jitter every time. stop() aborts the in-flight fetches,
// stops playback (including what is already scheduled) and discards the queue.
//
// Markdown, code blocks and URLs are flattened out for reading (plainify).

// The implementation lives in five files under parts/; this one is an index and holds no rules.
// The dependencies run one way and never back:
//   ttsOptions (settings -> TtsOptions)
//     -> ttsVoices (characters, voice pool, emotion styles)
//       -> ttsAudio (synthesis cache, AudioContext, output volume)
//         -> ttsPlay (startTts, global stop, serialized announcement queue)
//           -> ttsNarration (narration mode)
// Callers still import from "features/chat/tts.ts", as they did before the split.
export { stopTtsForReplacement } from "./ttsControl.ts";
export type { TtsController, TtsEndReason, TtsStopReason } from "./ttsControl.ts";
export * from "./parts/ttsOptions.ts";
export * from "./parts/ttsVoices.ts";
export * from "./parts/ttsAudio.ts";
export * from "./parts/ttsPlay.ts";
export * from "./parts/ttsNarration.ts";
