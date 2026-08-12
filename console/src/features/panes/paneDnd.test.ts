import { describe, expect, it } from "vitest";
import { acceptsPaneDrag, setTabDragShield, TAB_DRAGGING_CLASS, tabOwnsDrop } from "./paneDnd.ts";

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

  it("enables and removes the global View scroll shield", () => {
    const calls: string[] = [];
    const classes = { add: (name: string) => calls.push(`add:${name}`), remove: (name: string) => calls.push(`remove:${name}`) };
    setTabDragShield(true, classes);
    setTabDragShield(false, classes);
    expect(calls).toEqual([`add:${TAB_DRAGGING_CLASS}`, `remove:${TAB_DRAGGING_CLASS}`]);
  });
});
