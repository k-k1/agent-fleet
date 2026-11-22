// readTurn's karaoke highlight class is the caller's choice: mirror keeps `tts-active`, chat passes
// `chat-tts-active`, so each surface's own stylesheet (one file per class name) styles the block.
import { describe, it, expect, vi } from "vitest";

let onItem: ((i: number | null, r?: string) => void) | null = null;
vi.mock("../chat/tts.ts", () => ({
  BLOCK_BEAT: 0,
  SENT_BEAT: 0,
  startNarration: (_t: string[], _s: string, cb: typeof onItem) => {
    onItem = cb;
    return { pause() {}, resume() {}, stop() {} };
  },
}));

import { readTurn } from "./turnTts.ts";

function body() {
  const el = document.createElement("div");
  el.innerHTML = "<div class=\"markdown\"><p>First sentence here.</p><p>Second one.</p></div>";
  el.querySelectorAll("p").forEach((p) => ((p as HTMLElement).scrollIntoView = () => {}));
  return el;
}

describe("readTurn highlight class", () => {
  it("defaults to tts-active", () => {
    const b = body();
    readTurn(b, "src", 0, () => {});
    onItem!(0);
    expect(b.querySelectorAll(".tts-active")).toHaveLength(1);
  });
  it("uses the class the caller passes", () => {
    const b = body();
    readTurn(b, "src", 0, () => {}, undefined, "", "chat-tts-active");
    onItem!(0);
    expect(b.querySelectorAll(".chat-tts-active")).toHaveLength(1);
    expect(b.querySelectorAll(".tts-active")).toHaveLength(0);
    onItem!(null, "done");
    expect(b.querySelectorAll(".chat-tts-active")).toHaveLength(0);
  });
});
