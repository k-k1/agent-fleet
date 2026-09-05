// The "+N -M" on the changed-files band (docs/log/68) is counted by the Agent, because the list
// covers the whole transcript while the mirror holds only the trailing window. The same number
// is therefore counted in two places, Go and TypeScript, and fixing only one produces a failure
// that looks plausible on screen: the band's number disagrees with the diff opened from that row.
//
// This table is identical to TestEditStatMatchesLineDiff in
// workspace/agent/internal/transcript/files_test.go. Touch one and you must touch the other.
//
// It is a .dom.test despite mounting nothing because DiffView reaches settings → api/client,
// which touches localStorage and would fail at import time in the node environment.
import { describe, it, expect } from "vitest";
import { lineDiff } from "./DiffView.tsx";

const stat = (oldStr: string, newStr: string) => {
  const rows = lineDiff(oldStr, newStr);
  return {
    added: rows.filter((r) => r.t === "add").length,
    removed: rows.filter((r) => r.t === "del").length,
  };
};

describe("lineDiff added/removed counts (paired with the Agent's EditStat)", () => {
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
