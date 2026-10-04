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

describe("PendingQuestions translation", () => {
  const EN: Question[] = [{ question: "Which one?", options: [{ label: "Alpha" }, { label: "Beta" }] }];

  function mountTranslated(shown: boolean, onToggle = () => {}) {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() =>
      root!.render(
        <PendingQuestions
          questions={EN}
          sending={false}
          onSubmitKeys={(keys) => {
            sent.push(keys);
            return Promise.resolve(true);
          }}
          onSubmitSeq={(seq) => {
            sent.push(seq);
            return Promise.resolve(true);
          }}
          translate={{
            questions: shown ? [{ question: "どっち？", options: [{ label: "アルファ" }, { label: "ベータ" }] }] : EN,
            lead: "",
            shown,
            busy: false,
            toggle: onToggle,
          }}
        />,
      ),
    );
  }

  it("shows the translated text but answers with the original option", async () => {
    mountTranslated(true);
    expect(document.querySelector(".mq-text")!.textContent).toBe("どっち？");
    expect(opts().map((b) => b.querySelector(".mq-opt-label")!.textContent)).toEqual(["アルファ", "ベータ"]);
    click(opts()[1]);
    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    // Down×1, Enter — the keys aim at the option's POSITION, which the translation does not move.
    expect(sent).toEqual([["Down", "Enter"]]);
  });

  it("the button toggles through the wiring", () => {
    let pressed = 0;
    mountTranslated(false, () => pressed++);
    const btn = document.querySelector<HTMLButtonElement>(".mt-translate")!;
    expect(btn.classList.contains("on")).toBe(false);
    click(btn);
    expect(pressed).toBe(1);
  });
});

// A single-select pick and free text are mutually exclusive answers, but the pick must not
// erase what was typed: a user still weighing the options loses the text to one click.
describe("PendingQuestions free text kept under a pick", () => {
  const ONE: Question[] = [{ question: "どっち？", options: [{ label: "A" }, { label: "B" }] }];
  const inactive = () => texts()[0].classList.contains("inactive");

  it("a pick greys the typed text out instead of erasing it, and sends only the pick", async () => {
    mount(ONE);
    type(texts()[0], "迷い中のメモ");
    click(opts()[1]); // B
    expect(texts()[0].value).toBe("迷い中のメモ");
    expect(inactive()).toBe(true);
    expect(document.querySelector(".mq-freetext-note")).not.toBeNull();

    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    expect(sent).toEqual([["Down", "Enter"]]);
  });

  it("editing the kept text makes it the answer again and drops the pick", async () => {
    mount(ONE);
    type(texts()[0], "迷い中のメモ");
    click(opts()[1]);
    type(texts()[0], "やっぱりこれ");
    expect(picked()).toEqual([false, false]);
    expect(inactive()).toBe(false);

    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    expect(JSON.stringify(sent)).toContain("やっぱりこれ");
  });

  it("un-picking the option makes the kept text the answer again", () => {
    mount(ONE);
    type(texts()[0], "迷い中のメモ");
    click(opts()[1]);
    click(opts()[1]); // toggle B off
    expect(inactive()).toBe(false);
    expect(texts()[0].value).toBe("迷い中のメモ");
  });

  it("both survive the card being unmounted", () => {
    mount(ONE);
    type(texts()[0], "迷い中のメモ");
    click(opts()[0]);
    unmount();
    mount(ONE);
    expect(picked()).toEqual([true, false]);
    expect(texts()[0].value).toBe("迷い中のメモ");
    expect(inactive()).toBe(true);
  });

  // Every submit path must see the same "pick wins" rule: the builders on their own still
  // let text beat a pick (questionKeys.test.ts), so only the card's activeFree keeps the
  // greyed-out text from being sent.
  function mountWith(qs: Question[], extra: Partial<Parameters<typeof PendingQuestions>[0]>) {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() =>
      root!.render(
        <PendingQuestions
          questions={qs}
          draftKey={null}
          sending={false}
          onSubmitKeys={(keys) => {
            sent.push(keys);
          }}
          onSubmitSeq={(seq) => {
            sent.push(seq);
          }}
          {...extra}
        />,
      ),
    );
  }
  const submitNow = async () => {
    click(document.querySelector(".mq-submit"));
    await act(async () => {});
  };

  it("a carried answer carries the pick without the inactive text as notes", async () => {
    const answers: unknown[] = [];
    mountWith(ONE, { onSubmitAnswers: (a) => void answers.push(a) });
    type(texts()[0], "迷い中のメモ");
    click(opts()[0]);
    await submitNow();
    expect(answers).toEqual([[{ labels: ["A"], notes: "" }]]);
  });

  it("a managed answer carries the pick without the inactive text", async () => {
    const answers: unknown[] = [];
    mountWith(ONE, { onRespond: (a) => void answers.push(a) });
    type(texts()[0], "迷い中のメモ");
    click(opts()[1]);
    await submitNow();
    expect(answers).toEqual([[{ options: [1] }]]);
  });

  it("an agy write-in menu sends the option, not the inactive text", async () => {
    mountWith(ONE, { answerMode: "menu", writeIn: true });
    type(texts()[0], "迷い中のメモ");
    click(opts()[1]);
    await submitNow();
    expect(JSON.stringify(sent)).not.toContain("迷い中のメモ");
    expect(sent).toEqual([[{ k: "Down" }, { k: "Enter" }]]);
  });

  it("multi-select still sends checked options AND the text", async () => {
    const answers: unknown[] = [];
    mountWith([{ question: "どれ？", multiSelect: true, options: [{ label: "X" }, { label: "Y" }] }], {
      onRespond: (a) => void answers.push(a),
    });
    type(texts()[0], "ほかにも");
    click(opts()[0]);
    expect(inactive()).toBe(false);
    await submitNow();
    expect(answers).toEqual([[{ options: [0], text: "ほかにも" }]]);
  });

  it("whitespace-only text is not shown as kept", () => {
    mount(ONE);
    type(texts()[0], "  \n");
    click(opts()[0]);
    expect(inactive()).toBe(false);
    expect(document.querySelector(".mq-freetext-note")).toBeNull();
  });

  it("the note is announced on the greyed-out field", () => {
    mount(ONE);
    type(texts()[0], "迷い中のメモ");
    click(opts()[0]);
    const id = texts()[0].getAttribute("aria-describedby");
    expect(id && document.getElementById(id)?.classList.contains("mq-freetext-note")).toBe(true);
  });
});
