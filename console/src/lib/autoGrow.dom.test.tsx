import { describe, it, expect } from "vitest";
import { autoGrowTextarea } from "./autoGrow.ts";

// jsdom has no layout, so "the transcript does not drift" cannot be asserted here (that belongs
// to the scripts/mirror-scroll typing scenario, on a real Chromium). What is pinned here is the
// one condition the fix rests on: at the instant the input is shrunk for measurement, the parent
// row is held by min-height. Lose that and the shrink leaks into the sibling transcript.
function composer(): { row: HTMLDivElement; input: HTMLTextAreaElement } {
  const row = document.createElement("div");
  const input = document.createElement("textarea");
  row.appendChild(input);
  document.body.appendChild(row);
  return { row, input };
}

describe("autoGrowTextarea", () => {
  it("holds the parent row with min-height while measuring and restores it afterwards", () => {
    const { row, input } = composer();
    input.value = "a long draft";
    row.getBoundingClientRect = () => ({ height: 212 }) as DOMRect;
    const seen: string[] = [];
    // Reading scrollHeight is the moment of measurement; record the parent's min-height then.
    Object.defineProperty(input, "scrollHeight", {
      get() {
        seen.push(row.style.minHeight);
        return 260;
      },
    });

    autoGrowTextarea(input);

    expect(seen).toEqual(["212px"]); // the shrunken state does not leak out
    expect(input.style.height).toBe("260px");
    expect(row.style.minHeight).toBe(""); // cleaned up, so the next layout is not constrained
  });

  it("restores a min-height the parent already had", () => {
    const { row, input } = composer();
    input.value = "a";
    row.style.minHeight = "54px";
    row.getBoundingClientRect = () => ({ height: 54 }) as DOMRect;
    Object.defineProperty(input, "scrollHeight", { get: () => 38 });

    autoGrowTextarea(input);

    expect(row.style.minHeight).toBe("54px");
  });

  it("gives an empty input its rows height instead of measuring the placeholder", () => {
    const { input } = composer();
    input.placeholder = "a placeholder that wraps to several lines at a narrow width";
    input.style.height = "75px";
    let measured = false;
    Object.defineProperty(input, "scrollHeight", {
      get() {
        measured = true;
        return 75;
      },
    });

    autoGrowTextarea(input);

    expect(measured).toBe(false);
    expect(input.style.height).toBe("");
  });

  it("does nothing without an element (before mount / after unmount)", () => {
    expect(() => autoGrowTextarea(null)).not.toThrow();
  });
});
