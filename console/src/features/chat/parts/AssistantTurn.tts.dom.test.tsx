// Manual read-aloud on a finished chat reply highlights the spoken block with the chat surface's own
// class (chat-tts-active, styled in bubble.css), not mirror's tts-active. The class travels
// AssistantTurn -> readTurn, so this pins the call-site wiring that the readTurn unit test cannot.
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

let onItem: ((i: number | null, r?: string) => void) | null = null;
vi.mock("../tts.ts", () => ({
  BLOCK_BEAT: 0,
  SENT_BEAT: 0,
  startNarration: (_t: string[], _s: string, cb: typeof onItem) => {
    onItem = cb;
    return { pause() {}, resume() {}, stop() {} };
  },
}));
vi.mock("../../../lib/settings.ts", async (orig) => {
  const m = await orig<typeof import("../../../lib/settings.ts")>();
  return { ...m, useSettings: () => ({ ...m.getSettings(), ttsEnabled: true }) };
});

import { AssistantTurn } from "./AssistantTurn.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";

describe("AssistantTurn read-aloud highlight", () => {
  it("lights the spoken paragraph with chat-tts-active", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    act(() => root.render(<ToastProvider><AssistantTurn text={"First paragraph here.\n\nSecond paragraph."} ts={0} agentName="a" /></ToastProvider>));
    host.querySelectorAll("p").forEach((p) => ((p as HTMLElement).scrollIntoView = () => {}));
    const read = host.querySelector<HTMLButtonElement>(".chat-msg-foot button.cm-copy")!;
    act(() => read.click());
    expect(onItem).not.toBeNull();
    act(() => onItem!(0));
    expect(host.querySelectorAll(".chat-body .chat-tts-active")).toHaveLength(1);
    expect(host.querySelectorAll(".tts-active")).toHaveLength(0);
    act(() => root.unmount());
  });
});
