import { describe, expect, it } from "vitest";
import { kvCacheMiB, modelFit, windowThatFits } from "./engineFit.ts";
import type { EngineClass } from "./engineTypes.ts";

const rung = (id: string, vram_mib: number): EngineClass => ({ id, label: id, vram_mib, types: [] });
const LADDER = [rung("g6.xlarge", 22000), rung("g6e.xlarge", 46068), rung("g6e.2xlarge", 46068)];

// 🔴 The measured case this ADR started from: unsloth/Qwen3.8-27B-GGUF, read off the real GGUF
// headers on 2026-09-18. 65 blocks x 4 KV heads x (256+256) x 2 bytes = 260 MiB per 1,024 tokens,
// and the model publishes a 262,144 ceiling — which is where the operator's "KV キャッシュ
// 66560 MiB" came from.
const QWEN_KV_PER_1K = 260;

describe("kvCacheMiB", () => {
  it("reproduces the number the panel showed at the model's published ceiling", () => {
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
