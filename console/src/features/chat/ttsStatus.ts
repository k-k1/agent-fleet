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
import { api, apiJSON, errText } from "../../core/api/client.ts";
import type { TtsStatus } from "./ttsAvailability.ts";

export type { TtsProviderStatus, TtsStatus } from "./ttsAvailability.ts";
export { voicevoxAvailable, pollyAvailable, engineActivity } from "./ttsAvailability.ts";

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

// refreshTtsStatus ignores the TTL. Under an on-demand engine (ADR 0070) the state moves on
// its own — a start takes 70-80 seconds — so a screen that is watching one has to be able to
// ask again before the 30 seconds are up.
export function refreshTtsStatus(): Promise<TtsStatus | null> {
  at = 0;
  return loadTtsStatus();
}

// noteTtsStatus records a status body that arrived some other way (the wake response), so
// pressing "call Zundamon" does not need a second round trip to show the new state.
function noteTtsStatus(d: unknown): TtsStatus | null {
  const p = (d as { providers?: Record<string, unknown> } | null)?.providers;
  if (!p || typeof p !== "object") return null;
  cache = {
    voicevox: (p.voicevox as TtsStatus["voicevox"]) ?? { ready: false },
    polly: (p.polly as TtsStatus["polly"]) ?? { ready: false },
  };
  at = Date.now();
  return cache;
}

// wakeEngine asks the deployment to start the engine now (ADR 0070 decision 4). It is for the
// member who wants the voice for their own notifications: the automatic trigger needs 2,000
// characters inside five minutes, which a few announcements a day never reach.
//
// It costs money, so the CP audits and rate-limits it; a refusal is a message to show, never
// something to retry. started=false is a success too — it means the engine was already on its
// way and the press only said "somebody is listening".
export async function wakeEngine(): Promise<{ ok: boolean; started: boolean; message: string; status: TtsStatus | null }> {
  const d = await apiJSON("api/tts/wake", "POST").catch(() => null);
  if (!d || d.error) return { ok: false, started: false, message: errText(d?.error) || "", status: null };
  return { ok: true, started: d.started === true, message: "", status: noteTtsStatus(d) };
}
