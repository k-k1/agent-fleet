// 変更ファイル帯（docs/68）の「+N −M」は Agent が集計する——一覧が全転写ぶんで、
// ミラーが持っているのは末尾の窓だけだから。つまり同じ数を **Go と TypeScript の
// 2 箇所**が数えることになり、片方だけ直せば「帯の数字と、その行を開いた差分の中身が
// 食い違う」という、画面上はもっともらしい壊れ方をする。
//
// この表は workspace/agent/internal/transcript/files_test.go の
// TestEditStatMatchesLineDiff と同一。どちらかを触ったら両方を触ること。
//
// 何も mount しないのに .dom.test なのは、DiffView が settings → api/client を辿って
// localStorage に触るため（node 環境では import 時点で落ちる）。
import { describe, it, expect } from "vitest";
import { lineDiff } from "./DiffView.tsx";

const stat = (oldStr: string, newStr: string) => {
  const rows = lineDiff(oldStr, newStr);
  return {
    added: rows.filter((r) => r.t === "add").length,
    removed: rows.filter((r) => r.t === "del").length,
  };
};

describe("lineDiff の増減数（Agent 側 EditStat と対）", () => {
  const cases: [string, string, string, number, number][] = [
    ["unchanged", "a\nb\nc", "a\nb\nc", 0, 0],
    ["one line replaced", "a\nb\nc", "a\nB\nc", 1, 1],
    ["pure insert", "a\nc", "a\nb\nc", 1, 0],
    ["pure delete", "a\nb\nc", "a\nc", 0, 1],
    ["write (no old side)", "", "a\nb", 2, 0],
    ["delete (no new side)", "a\nb", "", 0, 2],
    ["both empty", "", "", 0, 0],
    ["trailing newline is not a line", "a\n", "a\nb\n", 1, 0],
    ["reordered keeps the longest common run", "a\nb\nc\nd", "b\nc\nd\na", 1, 1],
  ];
  for (const [name, o, n, added, removed] of cases) {
    it(name, () => expect(stat(o, n)).toEqual({ added, removed }));
  }
});
