// features/chat/ttsStatus - client cache of engine reachability (GET /api/tts/status).
//
// The single fetch point for whether this deployment has VOICEVOX (Zundamon) at all, used to decide
// whether the settings screen shows the Zundamon-related items. On a deployment with no VOICEVOX
// (the ECS default leaves AF_TTS_ECS_SERVICE unset, so no engine exists) the speaker, character and
// emotion-style pickers would all be selectable and have no effect, and auto falls back to Polly
// even for Japanese.
//
// Kept apart from ttsSpeakers (the real catalogue) because a null there cannot distinguish "not
// fetched yet" from "no engine"; the evidence for that decision belongs on the status side. The
// decision itself (voicevoxAvailable) lives in ttsAvailability.ts so it can be tested under node.
import { api } from "../../core/api/client.ts";
import type { TtsStatus } from "./ttsAvailability.ts";

export type { TtsProviderStatus, TtsStatus } from "./ttsAvailability.ts";
export { voicevoxAvailable, pollyAvailable } from "./ttsAvailability.ts";

const TTL = 30_000; // an admin may start/stop the engine, so re-opening the settings screen catches up
let cache: TtsStatus | null = null;
let at = 0;
let inflight: Promise<TtsStatus | null> | null = null;

// ttsStatusCache is the synchronous getter (null when not fetched or expired), for React initial state.
export function ttsStatusCache(): TtsStatus | null {
  return Date.now() - at < TTL ? cache : null;
}

export async function loadTtsStatus(): Promise<TtsStatus | null> {
  const fresh = ttsStatusCache();
  if (fresh) return fresh;
  if (!inflight) {
    inflight = api("api/tts/status")
      .then((d) => {
        const p = d?.providers;
        if (p && typeof p === "object") {
          cache = { voicevox: p.voicevox ?? { ready: false }, polly: p.polly ?? { ready: false } };
          at = Date.now();
        }
        return cache;
      })
      .catch(() => null) // cannot fetch (old CP, offline) -> decide nothing and show everything as before
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}
