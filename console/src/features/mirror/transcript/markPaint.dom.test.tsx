import { beforeEach, describe, expect, it } from "vitest";
import { paintTurnMarks } from "./markPaint.ts";
import type { TranscriptMark } from "./marks.ts";

function mark(over: Partial<TranscriptMark>): TranscriptMark {
  return { id: "mk_1", turn: "uuid-1", part: 0, kind: "text", quote: "target", nth: 0, color: "yellow", ...over };
}

function body(): HTMLElement {
  const el = document.createElement("div");
  el.innerHTML =
    '<div class="markdown" data-mark-root="uuid-1#0" data-mark-kind="text"><p>a target and a target again</p></div>' +
    '<div class="markdown" data-mark-root="uuid-1#1" data-mark-kind="text"><p>b target here</p></div>';
  document.body.appendChild(el);
  return el;
}

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("paintTurnMarks", () => {
  // ⚠️ 出現番号は root ひとつの中でだけ数える。ページ全体で数えると、共有側で片方の part が
  // 落ちたときに別の場所へ印が付く（docs/69 §69.3）。
  it("counts occurrences inside one root only", () => {
    const el = body();
    const byRoot = new Map([["uuid-1#0", [mark({ nth: 1 })]]]);
    paintTurnMarks(el, byRoot, () => 0, "");

    const first = el.querySelector('[data-mark-root="uuid-1#0"]')!;
    const second = el.querySelector('[data-mark-root="uuid-1#1"]')!;
    expect(second.querySelectorAll("mark.tmark")).toHaveLength(0);
    const painted = first.querySelectorAll("mark.tmark");
    expect(painted).toHaveLength(1);
    // 「2 番目の target」— 前後の文字がそれを裏づける。
    expect(first.textContent).toBe("a target and a target again");
    expect(painted[0].textContent).toBe("target");
    expect(first.innerHTML).toContain("and a <mark");
  });

  it("carries the colour and the author slot as classes, and the id for the card", () => {
    const el = body();
    const byRoot = new Map([["uuid-1#0", [mark({ color: "pink", author: "b@example.com" })]]]);
    paintTurnMarks(el, byRoot, () => 3, "");
    const painted = el.querySelector<HTMLElement>("mark.tmark")!;
    expect(painted.className).toBe("tmark tmark-pink tmark-a3");
    expect(painted.dataset.markId).toBe("mk_1");
  });

  it("does nothing at all for a turn with no marks (the common case, every poll)", () => {
    const el = body();
    const before = el.innerHTML;
    expect(paintTurnMarks(el, new Map(), () => 0, "")).toBe("");
    expect(el.innerHTML).toBe(before);
  });

  // 本文が作り直されて印が消えた回（テーマ変更など）は、指紋が同じでも塗り直す。
  it("repaints when the body was re-rendered underneath and the marks are gone", () => {
    const el = body();
    const byRoot = new Map([["uuid-1#0", [mark({})]]]);
    const sig = paintTurnMarks(el, byRoot, () => 0, "");
    expect(el.querySelectorAll("mark.tmark")).toHaveLength(1);

    const root = el.querySelector<HTMLElement>('[data-mark-root="uuid-1#0"]')!;
    root.innerHTML = "<p>a target and a target again</p>"; // MarkdownView が描き直した状態
    expect(el.querySelectorAll("mark.tmark")).toHaveLength(0);

    expect(paintTurnMarks(el, byRoot, () => 0, sig)).toBe(sig);
    expect(el.querySelectorAll("mark.tmark")).toHaveLength(1);
  });

  it("leaves the text untouched when the quote no longer matches", () => {
    const el = body();
    const byRoot = new Map([["uuid-1#0", [mark({ quote: "vanished" })]]]);
    paintTurnMarks(el, byRoot, () => 0, "");
    const root = el.querySelector('[data-mark-root="uuid-1#0"]')!;
    expect(root.querySelectorAll("mark.tmark")).toHaveLength(0);
    expect(root.textContent).toBe("a target and a target again");
  });
});
