// Which route a pending question card takes, by kind and id. Pinned: an opencode question
// without an id (registry questionByIdOnly) sends no key at all — no menu keys and no Cancel,
// which on a Terminal card is an Escape — while one with an id answers and cancels through
// /respond, and a codex question without an id still drives its menu and cancels with Escape.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MirrorPendingCards } from "./MirrorPendingCards.tsx";
import type { Question } from "../transcript/types.ts";

type Props = Parameters<typeof MirrorPendingCards>[0];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const noop = () => {};
// Every plan action is unused by a question card; a Proxy stands in for the whole set.
const plan = new Proxy({}, { get: () => noop }) as Props["plan"];

function render(kind: string, pending: Question[]) {
  const actions = {
    sendKeys: vi.fn(() => Promise.resolve(true)),
    sendSeq: vi.fn(() => Promise.resolve(true)),
    sendInterrupt: vi.fn(() => Promise.resolve()),
    cancelQuestion: vi.fn(() => Promise.resolve()),
    sendApproval: vi.fn(),
    sendRespond: vi.fn(() => Promise.resolve(true)),
  };
  const st = {
    busy: false,
    queuedCount: 0,
    pending,
    pendingText: "",
    liveMode: null,
    liveText: "",
    pendingPlan: null,
    pendingPerm: null,
    pendingApproval: null,
    carried: null,
    setCarried: noop,
    markRejected: noop,
    sending: false,
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <MirrorPendingCards
        session={"s-" + kind}
        sessionMeta={{ name: "s-" + kind, kind } as Props["sessionMeta"]}
        managed={false}
        agentName={kind}
        toast={{} as Props["toast"]}
        translate={undefined as unknown as Props["translate"]}
        st={st as unknown as Props["st"]}
        actions={actions as unknown as Props["actions"]}
        plan={plan}
      />,
    ),
  );
  return actions;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  localStorage.clear();
});

const Q = (id?: string): Question[] => [{ id, question: "どっち？", options: [{ label: "A" }, { label: "B" }] } as Question];
const opts = () => Array.from(document.querySelectorAll<HTMLButtonElement>(".mq-opt"));
const cancel = () => document.querySelector<HTMLButtonElement>(".mq-cancel");
const submit = () => document.querySelector<HTMLButtonElement>(".mq-submit");
const click = async (el: Element | null) => {
  await act(async () => (el as HTMLElement).click());
};

describe("MirrorPendingCards question routes", () => {
  it("opencode without an id: read-only, no Cancel, no keys", async () => {
    const a = render("opencode", Q());
    expect(opts().every((b) => b.disabled)).toBe(true);
    expect(submit()).toBeNull();
    expect(cancel()).toBeNull();
    expect(document.querySelector(".mq-terminal-hint")).not.toBeNull();
    await click(opts()[1]);
    expect(a.sendKeys).not.toHaveBeenCalled();
    expect(a.sendSeq).not.toHaveBeenCalled();
    expect(a.sendInterrupt).not.toHaveBeenCalled();
  });

  it("opencode with an id: answers and cancels through /respond", async () => {
    const a = render("opencode", Q("frm_1"));
    await click(opts()[1]);
    await click(submit());
    expect(a.sendRespond).toHaveBeenCalledWith("frm_1", [{ options: [1] }]);
    await click(cancel());
    expect(a.cancelQuestion).toHaveBeenCalledWith("frm_1");
    expect(a.sendKeys).not.toHaveBeenCalled();
    expect(a.sendSeq).not.toHaveBeenCalled();
    expect(a.sendInterrupt).not.toHaveBeenCalled();
  });

  it("codex without an id: drives its menu, Cancel is the Escape", async () => {
    const a = render("codex", Q());
    await click(opts()[1]);
    await click(submit());
    expect(a.sendSeq).toHaveBeenCalledWith([{ k: "Down" }, { k: "Enter" }]);
    await click(cancel());
    expect(a.sendInterrupt).toHaveBeenCalledTimes(1);
  });
});
