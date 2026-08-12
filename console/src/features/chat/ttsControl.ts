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
// quietGain は "quiet" 時のマスター音量倍率（設定 ttsBackgroundVolume でスライダー調整）。
// 未指定は従来の固定値 HIDDEN_TTS_GAIN。mute は常に 0、normal・非背景は常に 1。
export function ttsMasterGain(mode: TtsBackgroundMode, background: boolean, quietGain = HIDDEN_TTS_GAIN): number {
  if (!background || mode === "normal") return 1;
  return mode === "mute" ? 0 : Math.max(0, Math.min(1, quietGain));
}

export const MAX_PANE_PAN = 0.7;

// 列の左右端を ±MAX_PANE_PAN に対応させ、その間は実際の列中心（colRatios を反映）で補間する。
// 上下に積まれたペインは同じ列なので同じ位置。単一列・対象外・設定OFFは中央へ戻す。
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
