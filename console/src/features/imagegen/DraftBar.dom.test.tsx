// The narrow pane's loop (lane L4): the draft bar offers exactly the presses the form would
// allow, and opens the settings tab from anywhere but its buttons; the tab marks count new
// pictures from the first list on, and clear once the member looks.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DraftBar } from "./parts/DraftBar.tsx";
import { emptyDraft, type ImagegenDraft } from "./draft.ts";
import { useArrivals, useChatMark, useUnseenCount, type ChatMark } from "./loop.ts";
import type { ResultItem } from "./parts/ResultCards.tsx";

let host: HTMLDivElement;
let root: Root;

const mount = async (node: React.ReactNode) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(node));
};
const rerender = async (node: React.ReactNode) => act(async () => root.render(node));

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

const TRIAL: ResultItem = {
  file: { path: "generated/console/t1.png", seed: 7 },
  job: { id: "j1", state: "done", trial: true },
};

function Bar({
  draft = { ...emptyDraft(), prompt: "a red fox\nin snow", model: "sdxl" },
  gate = { busy: false, trialFull: false, queueFull: false },
  highlight = false,
  trial = null as ResultItem | null,
  hits = [] as string[],
}: {
  draft?: ImagegenDraft;
  gate?: { busy: boolean; trialFull: boolean; queueFull: boolean };
  highlight?: boolean;
  trial?: ResultItem | null;
  hits?: string[];
}) {
  return (
    <DraftBar
      draft={draft}
      modelName={draft.model}
      highlight={highlight}
      trial={trial}
      gate={gate}
      onTrial={() => hits.push("trial")}
      onEnqueue={() => hits.push("enqueue")}
      onOpenForm={() => hits.push("form")}
      onZoom={(p) => hits.push("zoom:" + p)}
    />
  );
}

const btns = () => [...host.querySelectorAll<HTMLButtonElement>(".igen-draftbar-btn")];

describe("DraftBar", () => {
  it("shows the prompt as one line and the model", async () => {
    await mount(<Bar />);
    expect(host.querySelector(".igen-draftbar-prompt")!.textContent).toBe("a red fox in snow");
    expect(host.querySelector(".igen-draftbar-model")!.textContent).toContain("sdxl");
  });

  it("says the draft is empty instead of drawing nothing", async () => {
    await mount(<Bar draft={{ ...emptyDraft() }} />);
    expect(host.querySelector(".igen-draftbar-prompt")!.textContent).toMatch(/空|empty/);
  });

  it("presses trial and enqueue without also opening the settings tab", async () => {
    const hits: string[] = [];
    await mount(<Bar hits={hits} />);
    await act(async () => btns()[0].click());
    await act(async () => btns()[1].click());
    expect(hits).toEqual(["trial", "enqueue"]);
  });

  it("opens the settings tab from the rest of the bar, once per tap", async () => {
    const hits: string[] = [];
    await mount(<Bar hits={hits} />);
    await act(async () => host.querySelector<HTMLButtonElement>(".igen-draftbar-text")!.click());
    await act(async () => host.querySelector<HTMLElement>(".igen-draftbar")!.click());
    expect(hits).toEqual(["form", "form"]);
  });

  it("follows the form's rule for disabling the buttons", async () => {
    await mount(<Bar gate={{ busy: true, trialFull: false, queueFull: false }} />);
    expect(btns().map((b) => b.disabled)).toEqual([true, true]);
    await rerender(<Bar gate={{ busy: false, trialFull: true, queueFull: false }} />);
    expect(btns().map((b) => b.disabled)).toEqual([true, false]);
    await rerender(<Bar gate={{ busy: false, trialFull: false, queueFull: true }} />);
    expect(btns().map((b) => b.disabled)).toEqual([false, true]);
    await rerender(<Bar draft={{ ...emptyDraft(), op: "inpaint", mask: "" }} />);
    expect(btns().map((b) => b.disabled)).toEqual([true, true]);
  });

  it("the latest trial's thumbnail opens the lightbox, not the settings", async () => {
    const hits: string[] = [];
    await mount(<Bar trial={TRIAL} hits={hits} />);
    await act(async () => host.querySelector<HTMLButtonElement>(".igen-draftbar-thumb")!.click());
    expect(hits).toEqual(["zoom:generated/console/t1.png"]);
  });

  it("stands out while the agent's change is unseen", async () => {
    await mount(<Bar />);
    expect(host.querySelector(".igen-draftbar-hl")).toBeNull();
    await rerender(<Bar highlight />);
    expect(host.querySelector(".igen-draftbar-hl")).not.toBeNull();
    expect(host.querySelector(".igen-draftbar-dot")).not.toBeNull();
  });
});

function Probe({
  paths,
  ready,
  viewing,
  state,
  out,
}: {
  paths: string[];
  ready: boolean;
  viewing: boolean;
  state?: string;
  out: { unseen: number; arrived: number[]; mark: ChatMark };
}) {
  out.unseen = useUnseenCount(paths, ready, viewing);
  useArrivals(paths, ready, (n) => out.arrived.push(n));
  out.mark = useChatMark(state, viewing);
  return null;
}

describe("the tab marks", () => {
  it("counts pictures newer than the first list, and clears once looked at", async () => {
    const out = { unseen: 0, arrived: [] as number[], mark: "" as ChatMark };
    await mount(<Probe paths={[]} ready={false} viewing={false} out={out} />);
    // The first list read is the baseline, however many pictures it holds.
    await rerender(<Probe paths={["a", "b"]} ready viewing={false} out={out} />);
    expect(out.unseen).toBe(0);
    expect(out.arrived).toEqual([]);
    await rerender(<Probe paths={["e", "d", "c", "a", "b"]} ready viewing={false} out={out} />);
    expect(out.unseen).toBe(3);
    expect(out.arrived).toEqual([3]);
    // A new array of the same pictures is not an arrival.
    await rerender(<Probe paths={["e", "d", "c", "a", "b"]} ready viewing={false} out={out} />);
    expect(out.arrived).toEqual([3]);
    await rerender(<Probe paths={["e", "d", "c", "a", "b"]} ready viewing out={out} />);
    expect(out.unseen).toBe(0);
    await rerender(<Probe paths={["f", "e", "d", "c", "a", "b"]} ready viewing={false} out={out} />);
    expect(out.unseen).toBe(1);
    expect(out.arrived).toEqual([3, 1]);
  });

  it("marks a turn that ended while the member was elsewhere, until they look", async () => {
    const out = { unseen: 0, arrived: [] as number[], mark: "" as ChatMark };
    await mount(<Probe paths={[]} ready viewing={false} state="idle" out={out} />);
    expect(out.mark).toBe("");
    await rerender(<Probe paths={[]} ready viewing={false} state="working" out={out} />);
    expect(out.mark).toBe("working");
    await rerender(<Probe paths={[]} ready viewing={false} state="idle" out={out} />);
    expect(out.mark).toBe("reply");
    await rerender(<Probe paths={[]} ready viewing state="idle" out={out} />);
    expect(out.mark).toBe("");
    await rerender(<Probe paths={[]} ready viewing={false} state="question" out={out} />);
    expect(out.mark).toBe("ask");
  });

  it("a question withdrawn back to idle is not a reply", async () => {
    const out = { unseen: 0, arrived: [] as number[], mark: "" as ChatMark };
    await mount(<Probe paths={[]} ready viewing={false} state="working" out={out} />);
    await rerender(<Probe paths={[]} ready viewing={false} state="question" out={out} />);
    expect(out.mark).toBe("ask");
    await rerender(<Probe paths={[]} ready viewing={false} state="idle" out={out} />);
    expect(out.mark).toBe("");
    await rerender(<Probe paths={[]} ready viewing={false} state="permission" out={out} />);
    await rerender(<Probe paths={[]} ready viewing={false} state="" out={out} />);
    expect(out.mark).toBe("");
  });

  it("a turn that ends while the conversation is on screen leaves no mark", async () => {
    const out = { unseen: 0, arrived: [] as number[], mark: "" as ChatMark };
    await mount(<Probe paths={[]} ready viewing state="working" out={out} />);
    await rerender(<Probe paths={[]} ready viewing state="idle" out={out} />);
    await rerender(<Probe paths={[]} ready viewing={false} state="idle" out={out} />);
    expect(out.mark).toBe("");
  });
});
