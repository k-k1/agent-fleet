// The answered question card in the transcript carries its own translate button. What must
// hold: the translation replaces what is DRAWN, never what the answer is matched against — a
// translated label must still come out highlighted as the pick the user made in the original.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { QuestionBlock } from "./blocks.tsx";
import { questionTranslateSource } from "../questionTranslate.ts";
import { turnTranslateKey } from "../translate.ts";
import type { TranscriptTranslateWiring } from "../useTranslate.ts";
import type { Question } from "./types.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const QS: Question[] = [
  { header: "Deploy", question: "How do you want to handle that?", options: [{ label: "Redeploy now" }, { label: "Skip it" }] },
];
const SOURCE = questionTranslateSource(QS);
const KEY = turnTranslateKey([SOURCE]);
const TRANSLATED = "`Q1.header` 配備\n\n`Q1.text` どう進めますか？\n\n`Q1.O1.label` 今すぐ再配備\n\n`Q1.O2.label` 飛ばす";

function wiring(shown: boolean, toggled: string[][] = []): TranscriptTranslateWiring {
  return {
    lang: "ja",
    get: (t) => (t === SOURCE ? TRANSLATED : undefined),
    shown: (k) => shown && k === KEY,
    busy: () => false,
    error: () => undefined,
    toggle: (_k, texts) => toggled.push(texts),
    auto: false,
    autoPress: () => {},
  };
}

function render(tx: TranscriptTranslateWiring | undefined) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<QuestionBlock questions={QS} answered answer={'"How do you want to handle that?"="Skip it"'} translate={tx} />));
  return host;
}

const labels = (h: HTMLElement) => Array.from(h.querySelectorAll(".mq-opt-label")).map((e) => e.textContent);

describe("QuestionBlock translation", () => {
  it("draws the translation and still highlights the original pick", () => {
    const h = render(wiring(true));
    expect(h.querySelector(".mq-text")!.textContent).toBe("どう進めますか？");
    expect(h.querySelector(".mq-header")!.textContent).toBe("配備");
    expect(labels(h)).toEqual(["今すぐ再配備", "飛ばす"]);
    expect(Array.from(h.querySelectorAll(".mq-opt")).map((b) => b.classList.contains("selected"))).toEqual([false, true]);
    expect(h.querySelector(".mt-translate")!.classList.contains("on")).toBe(true);
  });

  it("the button sends the whole card as one part", () => {
    const toggled: string[][] = [];
    const h = render(wiring(false, toggled));
    expect(labels(h)).toEqual(["Redeploy now", "Skip it"]);
    act(() => h.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    expect(toggled).toEqual([[SOURCE]]);
  });

  // #1114: the model left one field out of its reply, marker and all.
  describe("a field the reply dropped", () => {
    const DROPPED = "`Q1.header` 配備\n\n`Q1.text` どう進めますか？\n\n`Q1.O1.label` 今すぐ再配備";
    const REST = "`Q1.O2.label` Skip it";

    function dropping(shown: boolean, rest: string | undefined, pressed: Array<[string, string[]]>): TranscriptTranslateWiring {
      return {
        ...wiring(shown),
        get: (t) => (t === SOURCE ? DROPPED : t === REST ? rest : undefined),
        autoPress: (k, texts) => pressed.push([k, texts]),
      };
    }

    it("asks for just that field once more, showing the original meanwhile", () => {
      const pressed: Array<[string, string[]]> = [];
      const h = render(dropping(true, undefined, pressed));
      expect(labels(h)).toEqual(["今すぐ再配備", "Skip it"]);
      expect(pressed).toEqual([[turnTranslateKey([REST]), [REST]]]);
    });

    it("merges the follow-up over the first reply", () => {
      const pressed: Array<[string, string[]]> = [];
      const h = render(dropping(true, "`Q1.O2.label` 飛ばす", pressed));
      expect(labels(h)).toEqual(["今すぐ再配備", "飛ばす"]);
      expect(pressed).toEqual([]);
    });

    it("asks for nothing while the card is shown in the original", () => {
      const pressed: Array<[string, string[]]> = [];
      render(dropping(false, undefined, pressed));
      expect(pressed).toEqual([]);
    });
  });

  it("no wiring (the shared view) means no button", () => {
    const h = render(undefined);
    expect(h.querySelector(".mt-translate")).toBeNull();
  });
});
