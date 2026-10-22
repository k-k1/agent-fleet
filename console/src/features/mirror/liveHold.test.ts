// The reply being written is held across the poll between claude's Stop and the transcript row
// that replaces it (liveHold.ts). The hook's clock is covered in liveHold.dom.test.tsx.
import { describe, it, expect } from "vitest";
import { assistantTextSig, holdLive, LIVE_HOLD_IDLE, LIVE_HOLD_MS } from "./liveHold.ts";
import type { Turn } from "./transcript/types.ts";

const assistant = (...texts: string[]): Turn =>
  ({ role: "assistant", parts: texts.map((text) => ({ kind: "text", text })) }) as Turn;
const tool = (): Turn => ({ role: "assistant", parts: [{ kind: "tool", tool: "Bash" }] }) as Turn;

describe("assistantTextSig", () => {
  it("moves when assistant text lands, not for tools or user turns", () => {
    const base = [assistant("Reading.")];
    const sig = assistantTextSig(base);
    expect(assistantTextSig([...base, tool()])).toBe(sig);
    expect(assistantTextSig([...base, { role: "user", text: "hi", parts: [{ kind: "text", text: "hi" }] } as Turn])).toBe(sig);
    expect(assistantTextSig([...base, assistant("All done.")])).not.toBe(sig);
    expect(assistantTextSig([assistant("Reading. More")])).not.toBe(sig); // a part that grew
  });
});

describe("holdLive", () => {
  it("shows what the poll sends", () => {
    const h = holdLive(LIVE_HOLD_IDLE, "typing…", "1:5", 0);
    expect(h.text).toBe("typing…");
    expect(h.since).toBeNull();
    expect(holdLive(h, "typing…", "1:5", 10)).toBe(h); // unchanged: same object
  });

  it("holds the text when a poll drops it before the row lands", () => {
    const shown = holdLive(LIVE_HOLD_IDLE, "All done.", "1:5", 0);
    const held = holdLive(shown, "", "1:5", 100);
    expect(held.text).toBe("All done.");
    expect(held.since).toBe(100);
    expect(holdLive(held, "", "1:5", 100 + LIVE_HOLD_MS - 1).text).toBe("All done.");
  });

  it("lets go as soon as assistant text lands", () => {
    const held = holdLive(holdLive(LIVE_HOLD_IDLE, "All done.", "1:5", 0), "", "1:5", 100);
    expect(holdLive(held, "", "2:14", 200)).toBe(LIVE_HOLD_IDLE);
    // …also when the row arrives in the very poll that drops the text.
    expect(holdLive(holdLive(LIVE_HOLD_IDLE, "All done.", "1:5", 0), "", "2:14", 100)).toBe(LIVE_HOLD_IDLE);
  });

  it("gives up after LIVE_HOLD_MS when no row ever lands", () => {
    const held = holdLive(holdLive(LIVE_HOLD_IDLE, "cut off", "1:5", 0), "", "1:5", 100);
    expect(holdLive(held, "", "1:5", 100 + LIVE_HOLD_MS)).toBe(LIVE_HOLD_IDLE);
  });

  it("a poll that sends text again ends the hold", () => {
    const held = holdLive(holdLive(LIVE_HOLD_IDLE, "a", "1:5", 0), "", "1:5", 100);
    const back = holdLive(held, "a\nb", "1:5", 200);
    expect(back).toEqual({ text: "a\nb", sig: "1:5", since: null });
  });
});
