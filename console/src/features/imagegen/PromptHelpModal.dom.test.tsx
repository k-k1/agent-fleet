// Layer B's one hard rule, rendered (ADR 0081 decision 7): **the proposal is previewed, not
// applied.** ADR 0069 decision 7 forbids silent edits to the user's text, and the failure
// this guards is the ordinary one — a modal that helpfully writes the answer straight into
// the form the moment it arrives.
//
// `askAssistant` is mocked because the real one runs a CLI inside the workspace; what is
// under test is what the component does with an answer, not that the answer arrives.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const ask = vi.fn();
vi.mock("../chat/api.ts", () => ({ askAssistant: (p: string) => ask(p) }));

const { PromptHelpModal } = await import("./parts/PromptHelpModal.tsx");

let host: HTMLDivElement;
let root: Root;
let used: { prompt: string; negative: string | null }[] = [];

const render = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <PromptHelpModal
        model={{ id: "sdxl-base", family: "sdxl", knobs: ["negative"] }}
        loras={[]}
        alwaysNegative=""
        negativeReaches
        onUse={(prompt, negative) => used.push({ prompt, negative })}
        onClose={() => {}}
      />,
    );
  });
};

const byText = (t: string): HTMLButtonElement | undefined =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t));

const type = async (value: string) => {
  const box = document.querySelector(".igen-help textarea") as HTMLTextAreaElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(box, value);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

beforeEach(() => {
  used = [];
  ask.mockReset();
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("プロンプト支援", () => {
  it("押すまで尋ねない", async () => {
    await render();
    expect(ask).not.toHaveBeenCalled();
  });

  it("提案は表示されるだけで、自動では当たらない", async () => {
    ask.mockResolvedValue({ reply: '{"prompt":"a cat, masterpiece","negative":"blurry","note":"assumed daylight"}' });
    await render();
    await type("cat");
    await act(async () => byText("尋ねる")?.click());
    expect(document.body.textContent).toContain("a cat, masterpiece");
    expect(used).toEqual([]);
  });

  it("「使う」は両方、「プロンプトだけ使う」はネガティブを渡さない", async () => {
    ask.mockResolvedValue({ reply: '{"prompt":"a cat","negative":"blurry"}' });
    await render();
    await type("cat");
    await act(async () => byText("尋ねる")?.click());
    await act(async () => byText("プロンプトだけ使う")?.click());
    expect(used).toEqual([{ prompt: "a cat", negative: null }]);
  });

  it("読めない返事は失敗として出し、提案は作らない", async () => {
    ask.mockResolvedValue({ reply: "sorry, I cannot" });
    await render();
    await type("cat");
    await act(async () => byText("尋ねる")?.click());
    expect(document.querySelector(".igen-proposal")).toBeNull();
    expect(document.querySelector(".igen-err")?.textContent).toBeTruthy();
  });
});
