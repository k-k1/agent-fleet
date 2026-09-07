// The per-utterance provider pin and the cache it fixed (ADR 0070 decision 13).
//
// Under an on-demand engine, where auto routes changes several times a day and in the middle
// of a reading: the engine finishes starting while somebody is being read to. Two defects
// follow from that, and both are pinned here.
//
//   - The audio cache was keyed on the CONFIGURED provider, so "auto" meant Polly's voice
//     before the engine arrived and Zundamon's after, under the same key. The old comment
//     accepted hearing a stale voice "right after auto's routing changes"; on-demand turns
//     that from an edge case into every start.
//   - A reading that begins in Polly has to finish in Polly. The pin travels in its own
//     request field and NEVER as provider:"polly", because CP counts demand from the
//     configured provider — a pinned remainder that stopped counting would let the idle
//     window close on somebody who is still listening (decision 3).
import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("../../../core/api/client.ts", () => ({ rel: (p: string) => "http://x/" + p }));

import { makeProviderPin, synthToBuffer } from "./ttsAudio.ts";
import { type TtsOptions } from "./ttsOptions.ts";

// A decoded buffer stand-in: the LRU only ever looks at duration. The cache is on by
// default (ttsCacheSec 900), which is what the cache-key cases below depend on.
const buffer = () => ({ duration: 1 }) as unknown as AudioBuffer;
const ctx = { decodeAudioData: async () => buffer() } as unknown as AudioContext;
const signal = new AbortController().signal;

const opts = (o: Partial<TtsOptions> = {}): TtsOptions => ({
  provider: "auto",
  voice: "3",
  speed: 1,
  lang: "ja",
  ...o,
});

// bodies collects what was posted; provider decides what X-TTS-Provider comes back.
let bodies: Record<string, unknown>[] = [];
let provider = "polly";

beforeEach(() => {
  bodies = [];
  provider = "polly";
  vi.stubGlobal("fetch", async (_url: string, init: RequestInit) => {
    bodies.push(JSON.parse(String(init.body)));
    return {
      ok: true,
      arrayBuffer: async () => new ArrayBuffer(8),
      headers: { get: (k: string) => (k === "X-TTS-Provider" ? provider : null) },
    } as unknown as Response;
  });
});

describe("provider pin", () => {
  it("sends the pin in its own field and leaves the configured provider alone", async () => {
    const pin = makeProviderPin();
    expect(pin.pinned()).toBe(false);

    const first = await synthToBuffer(ctx, "一文目。", pin.opts(opts()), signal);
    pin.note(first);
    expect(pin.pinned()).toBe(true);

    await synthToBuffer(ctx, "二文目。", pin.opts(opts()), signal);
    expect(bodies[0]).toMatchObject({ provider: "auto", pin: "" });
    // The one that must not regress: provider stays "auto" (the member's setting, which is
    // what CP counts as demand) while pin carries where this utterance is being read.
    expect(bodies[1]).toMatchObject({ provider: "auto", pin: "polly" });
  });

  it("keys the cache on the pin, so the second sentence of a Polly reading is not a Zundamon hit", async () => {
    // Cache "同じ文。" while auto is Polly...
    await synthToBuffer(ctx, "同じ文。", opts(), signal);
    expect(bodies).toHaveLength(1);
    // ...an unpinned repeat is a cache hit, which is the whole point of the cache.
    await synthToBuffer(ctx, "同じ文。", opts(), signal);
    expect(bodies).toHaveLength(1);
    // ...but the same text inside a reading pinned to voicevox is different audio.
    await synthToBuffer(ctx, "同じ文。", opts({ pin: "voicevox" }), signal);
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toMatchObject({ pin: "voicevox" });
  });

  it("drops what auto cached once auto is seen to route somewhere else", async () => {
    await synthToBuffer(ctx, "起動前の文。", opts(), signal); // cached as Polly's voice
    expect(bodies).toHaveLength(1);

    // The engine arrives: the next unpinned auto request comes back from voicevox.
    provider = "voicevox";
    await synthToBuffer(ctx, "起動後の文。", opts(), signal);
    expect(bodies).toHaveLength(2);

    // The first sentence must NOT still play in Polly's voice from the cache — under
    // on-demand that is a voice change every listener hears at every start.
    await synthToBuffer(ctx, "起動前の文。", opts(), signal);
    expect(bodies).toHaveLength(3);
  });

  it("takes the pin from a cache hit too, and forgets it on reset", async () => {
    const pin = makeProviderPin();
    await synthToBuffer(ctx, "キャッシュ用。", opts(), signal); // fills the cache and the buffer's provider
    const hit = await synthToBuffer(ctx, "キャッシュ用。", opts(), signal);
    expect(bodies).toHaveLength(1);
    pin.note(hit);
    expect(pin.opts(opts()).pin).toBe("polly");
    pin.reset();
    expect(pin.opts(opts()).pin).toBeUndefined();
  });
});
