// The pure half of start-at-sign-in (signinStart.ts): when a page may ask the Control Plane to
// start the workspace. It imports nothing, so it runs in the node test project.

/** True for the optimistic transitions ("starting…", "stopping…") and the initial "…". */
const busy = (state: string): boolean => state.endsWith("…");

/** Input older than this no longer counts as "the member is here". */
export const INPUT_FRESH_MS = 60_000;
/** After one request, no tab asks again for this long. */
export const COOLDOWN_MS = 30 * 60_000;

/** armedNext folds one observed workspace state into "may this page start the workspace".
 *  null = nothing definite seen yet. Once false it stays false. */
export function armedNext(armed: boolean | null, state: string): boolean | null {
  if (armed === false) return false;
  if (state === "stopped") return armed ?? true;
  // The initial "…" and an unreadable state say nothing; they must not decide either way
  // before the first real observation.
  if (armed === null && (state === "unknown" || busy(state))) return null;
  // Anything else after the first observation (running, starting, a "…" transition the user
  // started, none) means this page is no longer looking at a cold workspace.
  return false;
}

export interface SigninGate {
  state: string;
  armed: boolean | null;
  visible: boolean;
  now: number;
  lastInputAt: number;
  lastRequestAt: number;
  requestedThisLoad: boolean;
}

export function shouldRequestSigninStart(g: SigninGate): boolean {
  if (g.requestedThisLoad || g.armed !== true || g.state !== "stopped" || !g.visible) return false;
  if (g.lastInputAt <= 0 || g.now - g.lastInputAt > INPUT_FRESH_MS) return false;
  if (g.lastRequestAt > 0 && g.now - g.lastRequestAt < COOLDOWN_MS) return false;
  return true;
}

