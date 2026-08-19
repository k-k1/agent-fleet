// 「誰が引いたマーカーか」が分かること（docs/69 §69.7）。本文の <mark> は下線の色でしか
// 作成者を示さないので、名前が読めるのはこの帯とカードだけ。
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MarkStrip } from "./MarkStrip.tsx";
import type { TranscriptMark } from "./marks.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";

function wiring(all: TranscriptMark[], over: Partial<TranscriptMarksWiring> = {}): TranscriptMarksWiring {
  const slots = new Map<string, number>([["", 0], ["b@example.com", 1]]);
  return {
    byRoot: new Map(),
    all,
    canEdit: true,
    add: vi.fn(),
    remove: vi.fn(),
    canRemove: () => true,
    authorLabel: (a) => a || "あなた",
    authorSlot: (a) => slots.get(a || "") ?? 0,
    find: (id) => all.find((m) => m.id === id),
    ...over,
  };
}

function mark(over: Partial<TranscriptMark>): TranscriptMark {
  return { id: "mk_1", turn: "u", part: 0, kind: "text", quote: "引用", nth: 0, color: "yellow", ...over };
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(node));
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("MarkStrip", () => {
  it("names the author of every mark, and separates the colour axis from the author axis", () => {
    const marks = wiring([
      mark({ id: "mk_1", quote: "所有者の引用", color: "yellow" }),
      mark({ id: "mk_2", quote: "共有先の引用", color: "yellow", author: "b@example.com" }),
    ]);
    const el = render(<MarkStrip marks={marks} storageKey="s1" />);
    // 畳まれていても件数と「2人」が読める。
    expect(el.textContent).toContain("2");

    act(() => el.querySelector<HTMLButtonElement>(".mirror-marks-toggle")!.click());
    const rows = [...el.querySelectorAll(".tmark-row")];
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("あなた");
    expect(rows[1].textContent).toContain("b@example.com");
    // 同じ色でも作成者の点は別（色に作成者を載せていない）。
    expect(rows[0].querySelector(".tmark-chip")!.className).toContain("tmark-yellow");
    expect(rows[1].querySelector(".tmark-chip")!.className).toContain("tmark-yellow");
    expect(rows[0].querySelector(".tmark-dot")!.className).toContain("tmark-a0");
    expect(rows[1].querySelector(".tmark-dot")!.className).toContain("tmark-a1");
  });

  it("hides the delete control for a mark this reader may not remove", () => {
    const marks = wiring([mark({ id: "mk_2", author: "b@example.com" })], { canRemove: () => false });
    const el = render(<MarkStrip marks={marks} storageKey="s2" />);
    act(() => el.querySelector<HTMLButtonElement>(".mirror-marks-toggle")!.click());
    expect(el.querySelector(".tmark-row-del")).toBeNull();
  });

  // ⚠️ 空の帯を出さない。「0 件」は「まだ誰も引いていない」と「この面では引けない」を
  // 区別できない。
  it("renders nothing at all when no mark exists", () => {
    const el = render(<MarkStrip marks={wiring([])} storageKey="s3" />);
    expect(el.querySelector(".mirror-marks")).toBeNull();
  });

  // 転写は tail 窓しか持たない。まだ読み込んでいないターンの印は画面に無いので、押せない
  // 行として出す（押しても何も起きない、にしない）。
  it("disables a row whose mark is not on screen", () => {
    const marks = wiring([mark({ id: "mk_far" })]);
    const el = render(<MarkStrip marks={marks} storageKey="s4" />);
    act(() => el.querySelector<HTMLButtonElement>(".mirror-marks-toggle")!.click());
    const row = el.querySelector<HTMLButtonElement>(".tmark-row-btn")!;
    expect(row.disabled).toBe(false);
    act(() => row.click());
    expect(row.disabled).toBe(true);
  });
});
