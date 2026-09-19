// engineFit — will this model run on the box this engine is set to buy (ADR 0089)?
//
// Its own module because the same three numbers are asked for in three places — the ingest form's
// one file, a repository's whole ladder of quantisations, and a registered row — and a second
// copy of the arithmetic is a second thing to be wrong.
//
// 🔴 Everything here is an ESTIMATE and the wording on screen says so. What it counts is the
// weights plus the KV cache. What it cannot count is llama.cpp's compute buffers and the CUDA
// context, which depend on the batch size and are not in any header. The failure that costs real
// money is recorded in ADR 0074: an L4 took 17 GB of weights, then died on
// `cudaMalloc failed: out of memory ... failed to allocate buffer for kv cache` — four minutes and
// one purchased GPU after the button. So the thresholds below leave room rather than answering
// "it fits" at 99%.
import type { EngineClass, EngineModel } from "./engineTypes.ts";

/** Above this fraction of the card the answer stops being "yes" and becomes "probably, watch it".
 *  Not a measurement — a reserve, for the two allocations this estimate cannot see. */
export const FIT_COMFORTABLE = 0.85;

export type FitState = "fits" | "tight" | "over" | "unknown";

export type Fit = {
  state: FitState;
  weightsMiB: number;
  /** 0 when no GGUF header could be read, which is NOT the same as a model with no cache —
   *  `state` is then `unknown` unless the weights alone already overflow the card. */
  kvMiB: number;
  needMiB: number;
  /** What fraction of the card this demand is, 0 when the class declares no VRAM. */
  used: number;
  /** The smallest rung that would hold it, when this one would not. Absent when the deployment
   *  declares no ladder, and when nothing on the ladder is big enough — "buy a bigger one" with
   *  no bigger one to buy is worse than saying only that it does not fit. */
  nextClass?: EngineClass;
};

export function kvCacheMiB(kvPer1k: number, contextTokens: number): number {
  if (!(kvPer1k > 0) || !(contextTokens > 0)) return 0;
  return Math.round((kvPer1k * contextTokens) / 1024);
}

export function modelFit(
  weightsMiB: number,
  kvPer1k: number,
  contextTokens: number,
  cardMiB: number,
  classes: EngineClass[] = [],
): Fit {
  const kvMiB = kvCacheMiB(kvPer1k, contextTokens);
  const needMiB = weightsMiB + kvMiB;
  const used = cardMiB > 0 ? needMiB / cardMiB : 0;
  // No card to compare against, or nothing to compare: the panel draws the numbers and no verdict.
  if (!(cardMiB > 0) || !(needMiB > 0)) {
    return { state: "unknown", weightsMiB, kvMiB, needMiB, used };
  }
  // 🔴 A missing KV figure is only "unknown" while the weights still fit. Once the weights ALONE
  // are over the card, no cache size can rescue it and the answer is knowable — which matters,
  // because a repository that will not answer a ranged GET would otherwise hide its largest
  // quantisations behind a shrug.
  if (kvMiB === 0 && kvPer1k <= 0 && needMiB <= cardMiB) {
    return { state: "unknown", weightsMiB, kvMiB, needMiB, used };
  }
  const state: FitState = used <= FIT_COMFORTABLE ? "fits" : used <= 1 ? "tight" : "over";
  if (state !== "over") return { state, weightsMiB, kvMiB, needMiB, used };
  const nextClass = [...classes]
    .filter((rung) => rung.vram_mib > cardMiB)
    .sort((left, right) => left.vram_mib - right.vram_mib)
    .find((rung) => needMiB <= rung.vram_mib * FIT_COMFORTABLE);
  return { state, weightsMiB, kvMiB, needMiB, used, nextClass };
}

/** The largest window this model can be started with and still sit comfortably on the card.
 *
 * Powers of two, because that is what everybody types and what the numbers on a model card are
 * written in — offering 37,491 would be arithmetically better and read as a machine talking to
 * itself.
 *
 * 🔴 This exists because the field used to be pre-filled with the model's published CEILING, and
 * the CP sends that value labelled "a ceiling, not a setting" (`engine_admin.go`, which notes the
 * 30B here publishes 262144 and is run at 32768). The panel then priced the KV cache at the
 * ceiling: 66,560 MiB for a 27B (measured 2026-09-18), which reads as "this will never run" for a
 * model that runs fine at 32768.
 *
 * Answers 0 when nothing can be said — no card, no cache figure — and the caller then falls back
 * to whatever it had.
 */
export function windowThatFits(weightsMiB: number, kvPer1k: number, cardMiB: number, ceiling: number): number {
  if (!(kvPer1k > 0) || !(cardMiB > 0) || !(ceiling > 0)) return 0;
  const room = cardMiB * FIT_COMFORTABLE - weightsMiB;
  if (room <= 0) return 0;
  let best = 0;
  for (let window = 1024; window <= ceiling; window *= 2) {
    if (kvCacheMiB(kvPer1k, window) > room) break;
    best = window;
  }
  return best;
}

/** The window to open a field at when the cache could NOT be sized — which is neither of the
 * two numbers that suggest themselves, because both are wrong in a way that only shows up
 * later:
 *
 *   - the model's CEILING (what `windowThatFits` used to fall back to) is the very thing ADR
 *     0089 exists to stop being typed in. Measured: the af-sandbox row left at 262,144 asked
 *     llama.cpp for 16 GiB of KV cache and the L4 answered `cudaMalloc failed: out of memory`.
 *   - ZERO reads as "undeclared" all the way down the chain, and opencode takes a context of 0
 *     as "auto-compaction off" (workspace/agent/.../opencode/engine.go) — the session then runs
 *     until llama-server rejects it, which is worse than a small window, not safer.
 *
 * So: the window every model in these deployments is actually started at, capped by the
 * model's own ceiling, and the caller SAYS it is a fallback rather than a fitted answer.
 */
export const WINDOW_WHEN_UNSIZED = 32768;

export function windowWhenUnsized(ceiling: number): number {
  return ceiling > 0 ? Math.min(WINDOW_WHEN_UNSIZED, ceiling) : WINDOW_WHEN_UNSIZED;
}

/** One row's answer to "what changes on this card". `to` is 0 when no window can be fitted —
 *  the weights alone fill the card — which is a REFUSAL to propose, not a window of nothing:
 *  0 travels as "undeclared" and opencode reads an undeclared context as auto-compaction off.
 *  `unknown` is the third answer: the row has no geometry or no ceiling, so nothing can be said
 *  until its header is read (which the CP does on the next loading write). */
export type WindowRefit = {
  id: string;
  from: number;
  to: number;
  unknown: boolean;
};

/** What every row's window becomes on a given card, for the rows where that is a CHANGE.
 *
 * The instance rung is the one input a window is fitted against, and until now it was read once,
 * when the model was registered. Changing the rung afterwards moved the card and left every
 * window behind — so an engine moved up to a 48 GB card went on running the 16k that fitted a
 * 24 GB one, and one moved DOWN kept a window its new card cannot hold and said nothing until
 * the cold start failed.
 *
 * LoRAs and image checkpoints are skipped: neither declares a window. A row whose fitted window
 * already equals its stored one is not returned at all — the caller's list is what CHANGED, and
 * a list that names every row every time is one nobody reads.
 */
export function refitWindows(models: EngineModel[], cardMiB: number): WindowRefit[] {
  if (!(cardMiB > 0)) return [];
  const out: WindowRefit[] = [];
  for (const model of models) {
    if (model.kind === "lora" || !(model.context_tokens && model.context_tokens > 0)) continue;
    const kvPer1k = model.kv_mib_per_1k_tokens || 0;
    const ceiling = model.context_length || 0;
    if (!kvPer1k || !ceiling) {
      out.push({ id: model.id, from: model.context_tokens, to: 0, unknown: true });
      continue;
    }
    // 🔴 What the weights cost has to be KNOWN, and two rows on the live deployments show why.
    // The operator's own measurement wins where there is one (it is what engineModelVramNeed
    // prefers, so using anything else here would fit against a number the guard disagrees
    // with). Where there is neither a measurement nor a byte count — the seeded
    // `qwen3-coder-30b-a3b` row is exactly that — summing the files gives 0, and 0 weights
    // means the whole card looks free: this would propose the model's ceiling for a model
    // nobody has weighed. Say "unknown" instead.
    const declared = model.vram_mib || 0;
    const fromFiles = Math.round((model.file_rows || []).reduce((sum, f) => sum + (f.bytes || 0), 0) / 1048576);
    const weightsMiB = declared > 0 ? declared : fromFiles;
    if (weightsMiB <= 0) {
      out.push({ id: model.id, from: model.context_tokens, to: 0, unknown: true });
      continue;
    }
    const to = windowThatFits(weightsMiB, kvPer1k, cardMiB, ceiling);
    if (to === model.context_tokens) continue;
    out.push({ id: model.id, from: model.context_tokens, to, unknown: false });
  }
  return out;
}

/** The output cap that travels with a window. Both or neither — the CP stores them together and
 *  a window written without one leaves the previous cap against a window it was not chosen for.
 *  An eighth is what the ingest form has always opened at. */
export function outputForWindow(window: number): number {
  return Math.floor(window / 8);
}
