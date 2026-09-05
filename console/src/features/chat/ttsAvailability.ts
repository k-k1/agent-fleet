// features/chat/ttsAvailability - decides whether this deployment has VOICEVOX (Zundamon) at all.
//
// Kept apart from the fetching side (ttsStatus.ts) because that one pulls in core/api/client, which
// touches localStorage at module scope and therefore cannot be imported from vitest's node
// environment. The decision lives here so it can be tested plainly.

export interface TtsProviderStatus {
  ready: boolean; // can synthesize right now
  enabled?: boolean; // admin toggle (voicevox only; false = routing stopped)
  managed?: boolean; // under ECS on-demand management (voicevox only)
  state?: string; // ECS service state when managed (running/starting/stopped)
}
export interface TtsStatus {
  voicevox: TtsProviderStatus;
  polly: TtsProviderStatus;
}

// voicevoxAvailable answers whether this deployment has a VOICEVOX engine. null = not known yet.
//
// The point is that this is availability, not readiness: under ECS on-demand management (managed)
// the engine counts as present even while stopped, because an admin can start it. Neither ready nor
// managed means no engine was ever provisioned, so Zundamon will never speak here. The admin toggle
// (enabled) is deliberately not consulted - that means "not right now", not "absent".
export function voicevoxAvailable(st: TtsStatus | null): boolean | null {
  if (!st) return null;
  return st.voicevox.ready || st.voicevox.managed === true;
}

// pollyAvailable answers whether Polly can be used on this deployment. null = not known yet.
//
// Unlike voicevox there is no managed (on-demand start) notion, and the CP's ready is exactly
// whether a region is configured (pollyProvider.Ready), so ready is availability.
//
// Without this check a deployment with no Polly still lists "Polly" among the engines and, with
// English selected as the reading language, shows a note that reads as "spoken in a Polly voice";
// in reality the CP's chooseTTSProvider sees plReady=false and falls back to voicevox, so what
// speaks is Zundamon (docs/log/84 §84.7).
export function pollyAvailable(st: TtsStatus | null): boolean | null {
  if (!st) return null;
  return st.polly.ready;
}
