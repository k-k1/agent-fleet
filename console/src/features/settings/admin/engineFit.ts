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
import type { EngineClass } from "./engineTypes.ts";

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
