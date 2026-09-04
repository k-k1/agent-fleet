// features/chat/ttsSpeakers - client cache of the engine's real catalogue (GET /api/tts/speakers,
// a proxy for VOICEVOX's /speakers). It is the single fetch point that lets the character setting,
// the session voice pool and the narration view's voice list run on the engine's actual speaker
// numbers; numbers held statically drift away from the engine (docs/log/24).
//
// Fetched once, on first reference. A failure (a 502 while the engine is stopped, say) is
// negative-cached for 30s so a retry does not fire on every read. Until it succeeds the cache stays
// null and the caller (voiceCharacters in tts.ts) runs on the static fallback.
import { api } from "../../core/api/client.ts";

export interface SpeakerStyle {
  id: string; // speaker number (the value passed as voice on a synthesis request)
  name: string; // style name, as the engine reports it
}
export interface Speaker {
  name: string; // character name (the key in the ttsVoicePool setting)
  styles: SpeakerStyle[]; // talking styles only; the CP has already dropped the singing ones
}

let cache: Speaker[] | null = null;
let inflight: Promise<Speaker[] | null> | null = null;
let failedAt = 0;

// speakersCatalog is the synchronous getter: it returns null while nothing is cached and kicks off
// the fetch in the background. From React, await loadSpeakers() and re-render on it.
export function speakersCatalog(): Speaker[] | null {
  if (!cache) void loadSpeakers();
  return cache;
}

export async function loadSpeakers(): Promise<Speaker[] | null> {
  if (cache) return cache;
  if (Date.now() - failedAt < 30_000) return null;
  if (!inflight) {
    inflight = api("api/tts/speakers")
      .then((d) => {
        const list = Array.isArray(d?.speakers) ? (d.speakers as Speaker[]) : null;
        if (list && list.length) cache = list;
        else failedAt = Date.now();
        return cache;
      })
      .catch(() => {
        failedAt = Date.now();
        return null;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}
