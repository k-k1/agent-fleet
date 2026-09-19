import { describe, expect, it } from "vitest";
import { kvCacheMiB, modelFit, windowThatFits, windowWhenUnsized, WINDOW_WHEN_UNSIZED, refitWindows, outputForWindow } from "./engineFit.ts";
import type { EngineClass, EngineModel } from "./engineTypes.ts";

const rung = (id: string, vram_mib: number): EngineClass => ({ id, label: id, vram_mib, types: [] });
const LADDER = [rung("g6.xlarge", 22000), rung("g6e.xlarge", 46068), rung("g6e.2xlarge", 46068)];

// 🔴 The figure the CP USED to send for unsloth/Qwen3.8-27B-GGUF, kept because these tests are
// about the panel's arithmetic and this is the number that produced the screens people saw.
// It is NOT the truth: 65 blocks x 4 KV heads x (256+256) x 2 bytes = 260 MiB per 1,024 tokens
// counts every block, and this architecture caches on 16 of them — one block is a NEXTN head
// llama.cpp never runs and only every 4th of the rest is full attention
// (`full_attention_interval`). Measured on af-sandbox 2026-09-18: at the published 262,144
// ceiling llama.cpp asked for `allocating 16384.00 MiB`, not 66,560. See
// control-plane/engine_gguf.go's cacheLayers.
const QWEN_KV_PER_1K = 260;
// What the same model answers once the modifiers are counted, for the fit cases that are about
// a real card rather than about multiplication.
const QWEN_KV_PER_1K_CORRECTED = 64;

describe("kvCacheMiB", () => {
  it("reproduces the number the panel USED to show at the published ceiling", () => {
    expect(kvCacheMiB(QWEN_KV_PER_1K, 262144)).toBe(66560);
  });

  it("is linear in the window, which is the whole point of storing it per 1k", () => {
    expect(kvCacheMiB(QWEN_KV_PER_1K, 32768)).toBe(8320);
    expect(kvCacheMiB(QWEN_KV_PER_1K, 8192)).toBe(2080);
  });

  it("answers 0 rather than guessing when either half is missing", () => {
    expect(kvCacheMiB(0, 32768)).toBe(0);
    expect(kvCacheMiB(QWEN_KV_PER_1K, 0)).toBe(0);
  });
});

describe("modelFit", () => {
  // UD-IQ2_XXS is 7.27 GB = 6,933 MiB. At 32,768 it needs 15,253 MiB of a 22,000 MiB card (69%),
  // which is the answer the operator could not get out of the old screen.
  it("says a 2-bit 27B fits a 22 GB card at a sane window", () => {
    const fit = modelFit(6933, QWEN_KV_PER_1K, 32768, 22000, LADDER);
    expect(fit.needMiB).toBe(15253);
    expect(fit.state).toBe("fits");
    expect(fit.nextClass).toBeUndefined();
  });

  it("says the same model does not fit at its published ceiling", () => {
    expect(modelFit(6933, QWEN_KV_PER_1K, 262144, 22000, LADDER).state).toBe("over");
  });

  // UD-IQ4_XS is 14.25 GB = 13,590 MiB: 21,910 of 22,000 is 99.6%, and the compute buffers this
  // estimate cannot see are what killed the L4 in ADR 0074. It must not read as a yes.
  it("calls 99% of the card tight, not a fit", () => {
    const fit = modelFit(13590, QWEN_KV_PER_1K, 32768, 22000, LADDER);
    expect(fit.state).toBe("tight");
  });

  it("names the smallest rung that would hold what this one will not", () => {
    const fit = modelFit(13590, QWEN_KV_PER_1K, 262144, 22000, LADDER);
    expect(fit.state).toBe("over");
    expect(fit.nextClass).toBeUndefined(); // 80,150 MiB fits on no rung this deployment declares
    const smaller = modelFit(20000, QWEN_KV_PER_1K, 32768, 22000, LADDER);
    expect(smaller.state).toBe("over");
    expect(smaller.nextClass?.id).toBe("g6e.xlarge");
  });

  it("stays unknown while no header could be read and the weights still fit", () => {
    expect(modelFit(6933, 0, 32768, 22000, LADDER).state).toBe("unknown");
  });

  // 🔴 …but not once the weights alone are over the card. A repository that will not answer a
  // ranged GET would otherwise hide its largest quantisations behind a shrug.
  it("still refuses weights that alone exceed the card, with no cache figure at all", () => {
    expect(modelFit(30000, 0, 32768, 22000, LADDER).state).toBe("over");
  });

  it("draws no verdict when the deployment declares no card", () => {
    expect(modelFit(6933, QWEN_KV_PER_1K, 32768, 0, LADDER).state).toBe("unknown");
  });
});

describe("windowThatFits", () => {
  it("picks the largest power of two that leaves room on the card", () => {
    // 22,000 x 0.85 = 18,700 of which the weights take 6,933, leaving 11,767 MiB:
    // 32,768 tokens cost 8,320 and 65,536 would cost 16,640.
    expect(windowThatFits(6933, QWEN_KV_PER_1K, 22000, 262144)).toBe(32768);
  });

  it("never goes past the model's own ceiling", () => {
    expect(windowThatFits(500, QWEN_KV_PER_1K, 46068, 16384)).toBe(16384);
  });

  it("answers 0 when the weights alone leave no room, rather than a window nothing can run", () => {
    expect(windowThatFits(21000, QWEN_KV_PER_1K, 22000, 262144)).toBe(0);
  });

  it("answers 0 when there is no cache figure to divide the room by", () => {
    expect(windowThatFits(6933, 0, 22000, 262144)).toBe(0);
  });
});

describe("windowWhenUnsized", () => {
  // 🔴 The two numbers that suggest themselves are both wrong, and this is the positive control
  // for not using either. The CEILING is what the field used to fall back to, and the af-sandbox
  // row left at 262,144 asked llama.cpp for 16 GiB of KV cache and the L4 answered
  // `cudaMalloc failed: out of memory`. ZERO reads as "undeclared" all the way to opencode,
  // which takes a context of 0 as "auto-compaction off" and runs until the engine rejects it.
  it("is neither the ceiling nor zero", () => {
    expect(windowWhenUnsized(262144)).toBe(WINDOW_WHEN_UNSIZED);
    expect(windowWhenUnsized(262144)).not.toBe(262144);
    expect(windowWhenUnsized(262144)).toBeGreaterThan(0);
  });

  it("never proposes more than the model was trained for", () => {
    expect(windowWhenUnsized(8192)).toBe(8192);
  });

  it("still answers when nothing is known about the ceiling", () => {
    expect(windowWhenUnsized(0)).toBe(WINDOW_WHEN_UNSIZED);
  });
});

// What the correction is worth on the card this deployment actually buys: the same 27B, the
// same L4, four times the window. The panel was telling operators a 22 GB card could hold 16k
// of a model that fits 65k on it.
describe("the corrected cache figure changes what the form offers", () => {
  const WEIGHTS_MIB = 11483; // Qwen3.8-27B-UD-IQ3_S, 12,040,883,104 bytes
  it("offers four times the window on the same card", () => {
    expect(windowThatFits(WEIGHTS_MIB, QWEN_KV_PER_1K, 22000, 262144)).toBe(16384);
    expect(windowThatFits(WEIGHTS_MIB, QWEN_KV_PER_1K_CORRECTED, 22000, 262144)).toBe(65536);
  });

  it("still calls the published ceiling over, which is what the row that OOMed declared", () => {
    expect(modelFit(WEIGHTS_MIB, QWEN_KV_PER_1K_CORRECTED, 262144, 22000, LADDER).state).toBe("over");
  });
});

describe("refitWindows", () => {
  const row = (over: Record<string, unknown> = {}) => ({
    id: "qwen", kind: "gguf", enabled: true,
    context_tokens: 32768, max_output_tokens: 8192,
    kv_mib_per_1k_tokens: QWEN_KV_PER_1K_CORRECTED, context_length: 262144,
    file_rows: [{ s3Key: "llm/qwen.gguf", bytes: 12_040_883_104 }],
    ...over,
  }) as unknown as EngineModel;

  // 🔥 The change this exists for. The rung is the one input a window is fitted against, and it
  // was read once at registration: an engine moved up to a 48 GB card went on running the window
  // that fitted a 24 GB one.
  it("grows the window when the card grows", () => {
    expect(refitWindows([row()], 22000)).toEqual([{ id: "qwen", from: 32768, to: 65536, unknown: false }]);
    expect(refitWindows([row()], 44000)).toEqual([{ id: "qwen", from: 32768, to: 262144, unknown: false }]);
  });

  // The direction that used to fail silently at the cold start instead of on screen.
  it("shrinks the window when the card shrinks", () => {
    const wide = row({ context_tokens: 262144 });
    expect(refitWindows([wide], 22000)).toEqual([{ id: "qwen", from: 262144, to: 65536, unknown: false }]);
  });

  it("says nothing about a row that is already right", () => {
    expect(refitWindows([row({ context_tokens: 65536 })], 22000)).toEqual([]);
  });

  // 🔴 to = 0 is a REFUSAL to propose, never a window to write: 0 travels as "undeclared" and
  // opencode reads an undeclared context as auto-compaction off.
  it("refuses rather than proposing zero when the weights alone fill the card", () => {
    expect(refitWindows([row()], 12000)).toEqual([{ id: "qwen", from: 32768, to: 0, unknown: false }]);
  });

  it("marks a row whose header was never read instead of guessing for it", () => {
    expect(refitWindows([row({ kv_mib_per_1k_tokens: undefined })], 22000))
      .toEqual([{ id: "qwen", from: 32768, to: 0, unknown: true }]);
    expect(refitWindows([row({ context_length: undefined })], 22000))
      .toEqual([{ id: "qwen", from: 32768, to: 0, unknown: true }]);
  });

  it("skips what has no window: LoRAs and image checkpoints", () => {
    expect(refitWindows([row({ kind: "lora" })], 22000)).toEqual([]);
    expect(refitWindows([row({ context_tokens: 0 })], 22000)).toEqual([]);
  });

  it("answers nothing at all when the deployment declares no card", () => {
    expect(refitWindows([row()], 0)).toEqual([]);
  });
});

describe("outputForWindow", () => {
  // Both or neither: the CP stores the pair together, and a window written without a cap leaves
  // the previous one against a window it was not chosen for.
  it("is an eighth, the same as the ingest form has always opened at", () => {
    expect(outputForWindow(65536)).toBe(8192);
    expect(outputForWindow(262144)).toBe(32768);
  });
});
