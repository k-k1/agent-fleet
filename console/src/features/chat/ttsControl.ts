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
export function ttsIsBackground(hidden: boolean, focused: boolean): boolean {
  return hidden || !focused;
}
export function ttsMasterGain(quietWhenBackground: boolean, background: boolean): number {
  return quietWhenBackground && background ? HIDDEN_TTS_GAIN : 1;
}

export const WORK_HUSHED_GAIN = 0.3;
export const WORK_WHISPER_GAIN = 0.58;
export function ttsWorkGain(mode: string): number {
  return mode === "hushed" ? WORK_HUSHED_GAIN : WORK_WHISPER_GAIN;
}

export const MAX_PANE_PAN = 0.7;

// 列の左右端を ±MAX_PANE_PAN に対応させ、その間は実際の列中心（colRatios を反映）で補間する。
// 上下に積まれたペインは同じ列なので同じ位置。単一列・対象外・設定OFFは中央へ戻す。
export function ttsPanePan(enabled: boolean, layout: Layout, paneId?: string): number {
  if (!enabled || !paneId || layout.cols.length < 2) return 0;
  const colIndex = layout.cols.findIndex((col) => col.panes.some((pane) => pane.id === paneId));
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
