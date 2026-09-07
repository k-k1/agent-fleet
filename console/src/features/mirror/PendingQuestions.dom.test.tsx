// The half-made answer must outlive the card's component. Selecting another tab of a pane
// unmounts the whole mirror (only the selected tab is mounted) and so does toggling to the
// terminal, so "pick an option, go and read the file it is about, come back" used to end on
// an empty card. This mounts, edits, UNMOUNTS and mounts again — the same sequence the tab
// switch performs — because the state is the component's and nothing else can prove it.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { PendingQuestions } from "./PendingQuestions.tsx";
import type { Question } from "./transcript/types.ts";

const QS: Question[] = [
  { question: "どっち？", options: [{ label: "A" }, { label: "B" }] },
  { question: "どれ？", multiSelect: true, options: [{ label: "X" }, { label: "Y" }] },
];
const KEY = "af.auq-draft.s1";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const sent: unknown[] = [];
let result: boolean | void = true;
let cancelled = 0;

function mount(qs: Question[] = QS, draftKey: string | null = KEY) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <PendingQuestions
        questions={qs}
        draftKey={draftKey}
        sending={false}
        onSubmitKeys={(keys) => {
          sent.push(keys);
          return Promise.resolve(result);
        }}
        onSubmitSeq={(seq) => {
          sent.push(seq);
          return Promise.resolve(result);
        }}
        onCancel={() => cancelled++}
      />,
    ),
  );
}

const unmount = () => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
};

const opts = () => Array.from(document.querySelectorAll<HTMLButtonElement>(".mq-opt"));
const texts = () => Array.from(document.querySelectorAll<HTMLTextAreaElement>(".mq-freetext"));
const click = (el: Element | null) => act(() => (el as HTMLElement).click());
// React owns the value property, so onChange only fires when the write goes through the
// native setter (same idiom as CarriedBlock.dom.test.tsx).
const type = (el: HTMLTextAreaElement, v: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const picked = () => opts().map((b) => b.classList.contains("checked"));

beforeEach(() => {
  localStorage.clear();
  sent.length = 0;
  cancelled = 0;
  result = true;
});

afterEach(() => {
  unmount();
  vi.unstubAllGlobals();
});

describe("PendingQuestions draft", () => {
  it("a pick and typed text come back after the card is unmounted and mounted again", () => {
    mount();
    click(opts()[1]); // B
    click(opts()[2]); // X (multi-select)
    type(texts()[1], "そのほか");
    expect(picked()).toEqual([false, true, true, false]);

    unmount();
    expect(picked()).toEqual([]); // the control: the card really is gone
    mount();

    expect(picked()).toEqual([false, true, true, false]);
    expect(texts()[1].value).toBe("そのほか");
    // Restored state is answerable state: the submit button must be live, not just filled in.
    expect(document.querySelector<HTMLButtonElement>(".mq-submit")!.disabled).toBe(false);
  });

  it("keeps nothing without a draft key (a card that must not persist)", () => {
    mount(QS, null);
    click(opts()[1]);
    expect(localStorage.length).toBe(0);
    unmount();
    mount(QS, null);
    expect(picked()).toEqual([false, false, false, false]);
  });

  it("a form the draft was not written for starts empty", () => {
    mount();
    click(opts()[1]);
    unmount();
    // Same session, next question — the stored answer belongs to the previous one and
    // pre-filling it here would be one click away from being sent unread.
    mount([{ question: "続ける？", options: [{ label: "はい" }, { label: "いいえ" }] }]);
    expect(picked()).toEqual([false, false]);
  });

  it("the draft goes away once the answer is sent", async () => {
    mount();
    click(opts()[1]);
    type(texts()[1], "そのほか");
    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    expect(sent).toHaveLength(1);

    unmount();
    mount();
    expect(picked()).toEqual([false, false, false, false]);
    expect(texts()[1].value).toBe("");
  });

  it("a refused send puts the draft back — the card stays, so its answer must too", async () => {
    result = false; // 400 bad_key / workspace down: nothing reached the pane
    mount();
    click(opts()[1]);
    type(texts()[1], "そのほか");
    click(document.querySelector(".mq-submit"));
    await act(async () => {});

    unmount();
    mount();
    expect(picked()).toEqual([false, true, false, false]);
    expect(texts()[1].value).toBe("そのほか");
  });

  it("the carried card picks up what was typed into the live one for the same question", () => {
    // The session stopping is what swaps the cards (docs/log/75): the live card goes, the
    // carried one takes its place with the identical question — and the answer half written
    // into the free-text row was being lost right there, at the moment the user is away from
    // the keyboard.
    mount();
    click(opts()[1]); // B
    type(texts()[1], "そのほか");
    unmount();

    mount(QS, "af.auq-carried-draft.s1");
    expect(picked()).toEqual([false, true, false, false]);
    expect(texts()[1].value).toBe("そのほか");

    // The card on screen owns it now: emptying the row there is not undone by the copy the
    // live card left behind.
    type(texts()[1], "");
    click(opts()[1]); // untoggle B
    unmount();
    mount(QS, "af.auq-carried-draft.s1");
    expect(picked()).toEqual([false, false, false, false]);
    expect(texts()[1].value).toBe("");
  });

  it("cancelling throws the draft away", () => {
    mount();
    click(opts()[1]);
    click(document.querySelector(".mq-cancel"));
    expect(cancelled).toBe(1);

    unmount();
    mount();
    expect(picked()).toEqual([false, false, false, false]);
  });
});
