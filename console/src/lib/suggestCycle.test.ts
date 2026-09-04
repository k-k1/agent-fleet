import { describe, it, expect } from "vitest";
import {
  suggestMatches,
  stepSuggestCycle,
  activeSuggestCycle,
  suggestFilterDraft,
  cycledSuggestion,
  type SuggestCycle,
} from "./suggestCycle.ts";

const CHIPS = ["ok, 進めて", "ok, やってみて", "ok cもいれよう", "commit して"];

describe("suggestMatches", () => {
  it("filters by prefix, case- and space-insensitively", () => {
    expect(suggestMatches("ok", CHIPS)).toEqual(["ok, 進めて", "ok, やってみて", "ok cもいれよう"]);
    expect(suggestMatches("OK, ", CHIPS)).toEqual(["ok, 進めて", "ok, やってみて"]);
    expect(suggestMatches("co", CHIPS)).toEqual(["commit して"]);
    expect(suggestMatches("zzz", CHIPS)).toEqual([]);
  });

  it("drops the typed text itself and duplicates", () => {
    expect(suggestMatches("ok", ["ok", "OK", "ok, 進めて", "ok, 進めて"])).toEqual(["ok, 進めて"]);
  });
});

describe("stepSuggestCycle", () => {
  it("walks the candidates, then returns to what the user typed", () => {
    let c = stepSuggestCycle(null, "ok", CHIPS, false);
    expect(c?.text).toBe("ok, 進めて");
    c = stepSuggestCycle(c, c!.text, CHIPS, false);
    expect(c?.text).toBe("ok, やってみて");
    c = stepSuggestCycle(c, c!.text, CHIPS, false);
    expect(c?.text).toBe("ok cもいれよう");
    c = stepSuggestCycle(c, c!.text, CHIPS, false); // full loop, back to the typed text
    expect(c?.text).toBe("ok");
    expect(c?.idx).toBe(0);
    c = stepSuggestCycle(c, c!.text, CHIPS, false);
    expect(c?.text).toBe("ok, 進めて");
  });

  it("goes backwards from the end with Shift+Tab", () => {
    const c = stepSuggestCycle(null, "ok", CHIPS, true);
    expect(c?.text).toBe("ok cもいれよう");
    expect(stepSuggestCycle(c, c!.text, CHIPS, true)?.text).toBe("ok, やってみて");
  });

  it("freezes the candidate list, so cycling doesn't re-filter on the completed text", () => {
    const c1 = stepSuggestCycle(null, "ok", CHIPS, false)!;
    // The input now holds the first candidate. Recomputing here would narrow the list to one
    // entry, but because it is frozen the next Tab moves to the second of the original three.
    const c2 = stepSuggestCycle(c1, c1.text, CHIPS, false)!;
    expect(c2.base).toBe("ok");
    expect(c2.items).toEqual(c1.items);
    expect(c2.text).toBe("ok, やってみて");
  });

  it("restarts when the draft was edited by hand", () => {
    const c1 = stepSuggestCycle(null, "ok", CHIPS, false)!;
    const c2 = stepSuggestCycle(c1, "co", CHIPS, false)!; // retyped
    expect(c2.base).toBe("co");
    expect(c2.text).toBe("commit して");
  });

  it("declines when there is nothing to complete", () => {
    expect(stepSuggestCycle(null, "zzz", CHIPS, false)).toBeNull();
    expect(stepSuggestCycle(null, "", CHIPS, false)).toBeNull(); // empty input keeps the old Tab (focus the chips)
    expect(stepSuggestCycle(null, "   ", CHIPS, false)).toBeNull();
    expect(stepSuggestCycle(null, "ok\n2行目", CHIPS, false)).toBeNull();
    expect(stepSuggestCycle(null, "ok", [], false)).toBeNull();
  });
});

describe("derived view state", () => {
  const cur: SuggestCycle = { base: "ok", items: CHIPS.slice(0, 2), idx: 1, text: "ok, 進めて" };

  it("keeps the chip row filtered by the frozen base while cycling", () => {
    expect(suggestFilterDraft(cur, "ok, 進めて")).toBe("ok");
    expect(cycledSuggestion(cur, "ok, 進めて")).toBe("ok, 進めて");
    expect(activeSuggestCycle(cur, "ok, 進めて")).toBe(cur);
  });

  it("falls back to the raw draft once the user types again", () => {
    expect(suggestFilterDraft(cur, "ok, 進めてから")).toBe("ok, 進めてから");
    expect(cycledSuggestion(cur, "ok, 進めてから")).toBeNull();
    expect(activeSuggestCycle(cur, "ok, 進めてから")).toBeNull();
  });

  it("highlights nothing when the ring is back on the typed text", () => {
    expect(cycledSuggestion({ ...cur, idx: 0, text: "ok" }, "ok")).toBeNull();
  });
});
