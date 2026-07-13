// Pure controller lifecycle contract shared by the TTS engine and UI call sites.
// Kept browser-independent so replacement-vs-explicit-stop behavior is unit-testable.

export type TtsStopReason = "explicit" | "replaced";
export type TtsEndReason = "done" | TtsStopReason;

export interface TtsController {
  push(delta: string): void;
  flush(): void;
  stop(reason?: TtsStopReason): void;
}

// Component lifecycle / turn-boundary cleanup is a replacement, not the user's global
// "be quiet" action. Keep this helper at call sites so an internal controller swap cannot
// accidentally clear announcement and mirror auto-read queues.
export function stopTtsForReplacement(c: TtsController | null | undefined): void {
  c?.stop("replaced");
}

export const HIDDEN_TTS_GAIN = 0.55;
export function ttsMasterGain(quietWhenHidden: boolean, hidden: boolean): number {
  return quietWhenHidden && hidden ? HIDDEN_TTS_GAIN : 1;
}
