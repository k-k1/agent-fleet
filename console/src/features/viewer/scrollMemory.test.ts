// 表示位置の記憶そのもの（キーの作り方と、溢れたときの捨て方）。
import { afterEach, describe, expect, it } from "vitest";
import { clearScrollPos, loadScrollPos, saveScrollPos, scrollMemoryKey } from "./scrollMemory.ts";

afterEach(() => clearScrollPos());

describe("scrollMemoryKey", () => {
  it("ペインとファイルの両方で分ける", () => {
    // 同じファイルを 2 枚のペインで開いて別々の場所を読める、が要件。
    expect(scrollMemoryKey("pane-1", "repos/x/a.go")).not.toBe(scrollMemoryKey("pane-2", "repos/x/a.go"));
    expect(scrollMemoryKey("pane-1", "repos/x/a.go")).not.toBe(scrollMemoryKey("pane-1", "repos/x/b.go"));
  });

  it("ファイルが無ければキーを作らない（＝記憶しない）", () => {
    expect(scrollMemoryKey("pane-1", "")).toBeNull();
  });

  it("ペイン id が無くてもキーは作る（ポップアウト等）", () => {
    expect(scrollMemoryKey(undefined, "repos/x/a.go")).not.toBeNull();
  });
});

describe("save/load", () => {
  it("控えた位置をそのまま返し、無ければ null", () => {
    saveScrollPos("k", 820);
    expect(loadScrollPos("k")).toBe(820);
    expect(loadScrollPos("other")).toBeNull();
  });

  it("先頭（0）も「覚えていない」とは区別する", () => {
    // 0 を落とすと、末尾まで読んで先頭へ戻した人が戻ってくるたび前の位置へ飛ばされる。
    saveScrollPos("k", 500);
    saveScrollPos("k", 0);
    expect(loadScrollPos("k")).toBe(0);
  });

  it("溢れたら古い順に捨て、最近使ったものは残す", () => {
    for (let i = 0; i < 200; i++) saveScrollPos(`k${i}`, i);
    saveScrollPos("k0", 999); // 触ったので「最近」に戻る
    saveScrollPos("new", 1);
    expect(loadScrollPos("k0")).toBe(999);
    expect(loadScrollPos("k1")).toBeNull(); // いちばん古いものが落ちた
    expect(loadScrollPos("new")).toBe(1);
  });
});
