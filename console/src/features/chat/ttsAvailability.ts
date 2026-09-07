// features/chat/ttsAvailability - decides whether this deployment has VOICEVOX (Zundamon) at all.
//
// Kept apart from the fetching side (ttsStatus.ts) because that one pulls in core/api/client, which
// touches localStorage at module scope and therefore cannot be imported from vitest's node
// environment. The decision lives here so it can be tested plainly.

export interface TtsProviderStatus {
  ready: boolean; // can synthesize right now (managed: reachable AND warmed up)
  enabled?: boolean; // admin intent as a boolean (voicevox only; false = routing stopped)
  mode?: string; // the admin intent itself: "off" | "on" | "ondemand" (voicevox only)
  managed?: boolean; // under ECS on-demand management (voicevox only)
  // What the deployment is doing right now, which is a different question from the mode:
  // running/starting/stopped, plus "stopping" for the window after "off" was pressed and
  // before the desired count moves (ADR 0070 decisions 5 and 7).
  state?: string;
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

// What a member is told the engine is doing, which is a different question from whether the
// deployment has one (ADR 0070 decisions 4 and 16):
//
//   unknown     nothing has been fetched yet — say nothing rather than guess
//   unavailable no VOICEVOX engine here, or an externally run one that is not answering
//   off         an administrator switched speech off; Polly reads and nobody can wake it
//   ready       Zundamon can read right now
//   warming     the container is up but has not loaded a voice model yet
//   starting    the task is being placed and the image pulled (70-80 s, measured)
//   stopped     scaled to zero, which under on-demand is the NORMAL state — wakeable
//
// ⚠️ warming is keyed on ready, never on the service state. ECS RUNNING only says the
// container started: /version answers 200 before any voice model is loaded, so a run that
// looks live still has to be read by Polly for another moment (decision 16).
export type TtsEngineActivity = "unknown" | "unavailable" | "off" | "ready" | "warming" | "starting" | "stopped";

export function engineActivity(st: TtsStatus | null): TtsEngineActivity {
  if (!st) return "unknown";
  const vv = st.voicevox;
  if (voicevoxAvailable(st) !== true) return "unavailable";
  // The intent comes first: while the mode is off, routing goes to Polly whatever the
  // engine happens to be doing, and an engine still draining is of no use to anyone.
  if (vv.mode === "off" || vv.enabled === false) return "off";
  if (vv.ready) return "ready";
  if (vv.managed !== true) return "unavailable"; // an external engine that stopped answering
  if (vv.state === "starting") return "starting";
  if (vv.state === "running") return "warming";
  return "stopped";
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
