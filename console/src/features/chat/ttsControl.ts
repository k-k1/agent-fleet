import type { Layout } from "../../layout/types.ts";

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

export const HIDDEN_TTS_GAIN = 0.35;
export type TtsBackgroundMode = "mute" | "quiet" | "normal";
export function ttsIsBackground(hidden: boolean, focused: boolean): boolean {
  return hidden || !focused;
}
// quietGain is the master volume factor used in "quiet" mode (the ttsBackgroundVolume slider).
// Omitted, it is the fixed HIDDEN_TTS_GAIN. mute is always 0; normal and non-background are always 1.
export function ttsMasterGain(mode: TtsBackgroundMode, background: boolean, quietGain = HIDDEN_TTS_GAIN): number {
  if (!background || mode === "normal") return 1;
  return mode === "mute" ? 0 : Math.max(0, Math.min(1, quietGain));
}

export const MAX_PANE_PAN = 0.7;

// Maps the leftmost and rightmost columns to ±MAX_PANE_PAN and interpolates between them by the
// actual column centres (which honour colRatios). Panes stacked vertically share a column and so
// share a position. A single column, a pane not found, or the setting off all return to centre.
export function ttsPanePan(enabled: boolean, layout: Layout, paneId?: string): number {
  if (!enabled || !paneId || layout.cols.length < 2) return 0;
  const colIndex = layout.cols.findIndex((col) => col.cells.some((cell) => cell.views.some((view) => view.id === paneId)));
  if (colIndex < 0) return 0;

  const rawWidths = layout.cols.map((_, i) => Math.max(0, layout.colRatios[i] ?? 0));
  const total = rawWidths.reduce((sum, width) => sum + width, 0);
  const widths = total > 0 ? rawWidths : layout.cols.map(() => 1);
  const widthTotal = total > 0 ? total : widths.length;
  let offset = 0;
  const centers = widths.map((width) => {
    const center = (offset + width / 2) / widthTotal;
    offset += width;
    return center;
  });
  const span = centers[centers.length - 1] - centers[0];
  if (span <= 0) return 0;
  const normalized = ((centers[colIndex] - centers[0]) / span) * 2 - 1;
  return Math.max(-MAX_PANE_PAN, Math.min(MAX_PANE_PAN, normalized * MAX_PANE_PAN));
}
