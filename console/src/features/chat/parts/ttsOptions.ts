import { getSettings } from "../../../lib/settings.ts";
import { getLocale } from "../../../lib/i18n/index.ts";
import { applyReadings } from "../ttsText.ts";

export interface TtsOptions {
  provider: string; // "auto" | "voicevox" | "polly"
  // The provider that answered the first sentence of the utterance being read, sent back on
  // every following one so a single answer is read in a single voice (ADR 0070 decision 13).
  // Not a setting: it is set per playback by makeProviderPin, never from settings.
  //
  // ⚠️ It is its own field, and a pin must never be expressed as provider:"polly" instead —
  // CP counts demand from the CONFIGURED provider, and a pinned remainder that stopped
  // counting would let the engine's idle window close on somebody who is still listening.
  pin?: string; // "" | "voicevox" | "polly"
  voice: string; // VOICEVOX speaker number
  speed: number; // speedScale
  enkana?: boolean; // pre-transcribe English into katakana (CP's enkana; voicevox only)
  pollyVoice?: string; // Polly VoiceId (also used as auto's fallback target)
  lang?: string; // language hint (ttsLang under Settings > Read aloud): "auto" | "ja" | "en"
  particlePause?: boolean; // setting ttsParticlePause; CP tightens comma pauses (voicevox only)
  volume?: number; // playback volume (0..1). Web Audio output gain, not a synthesis parameter
  paneId?: string; // originating pane; when enabled, its column position sets the stereo pan
}

// Shared construction of TtsOptions from settings (announce / speakText / startNarration /
// ChatView).
export function ttsOptsFromSettings(s = getSettings()): TtsOptions {
  // The Japanese-only reading fixups (enkana katakana English, particle pauses) apply only
  // when the UI locale is ja; an English UI goes straight to the plain voice and skips the
  // kana pipeline (docs/log/28 §2.4).
  const ja = getLocale() === "ja";
  return {
    provider: s.ttsProvider,
    voice: s.ttsVoiceVoicevox,
    speed: s.ttsSpeed,
    enkana: ja && s.ttsEnglishKana,
    pollyVoice: s.ttsVoicePolly,
    // A language axis of its own for read-aloud (docs/log/84). Borrowing the assistant's
    // answer language (outputLanguage) meant setting chat answers to English also switched
    // the mirror's reading and the narration view to Polly / Joanna. "auto" follows the UI
    // display language, the same axis as enkana and particle pauses above.
    lang: s.ttsLang === "auto" ? getLocale() : s.ttsLang,
    particlePause: ja && s.ttsParticlePause,
  };
}

// applyReadings (dictionary -> built-in reading fixups -> particle pauses) shapes Japanese
// pronunciation, so apply it only when the UI locale is ja. Non-ja returns the text as-is,
// assuming it is already trimmed (docs/log/28 §2.4).
export function localizedReadings(t: string, dict: [string, string][], particlePause: boolean): string {
  return getLocale() === "ja" ? applyReadings(t, dict, particlePause) : t;
}
