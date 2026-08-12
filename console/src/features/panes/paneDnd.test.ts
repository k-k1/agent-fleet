import { describe, expect, it } from "vitest";
import { acceptsPaneDrag, tabOwnsDrop } from "./paneDnd.ts";

describe("pane drag routing", () => {
  it("accepts a tab drag when the layout has only one cell", () => {
    expect(acceptsPaneDrag(false, false, true)).toBe(true);
    expect(acceptsPaneDrag(false, true, false)).toBe(false);
  });

  it("routes tab center drops to reorder and edge drops to split", () => {
    expect(tabOwnsDrop("center")).toBe(true);
    expect(tabOwnsDrop("right")).toBe(false);
    expect(tabOwnsDrop("down")).toBe(false);
  });
});
