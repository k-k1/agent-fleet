import { describe, it, expect } from "vitest";
import { isContextMenuKey, menuAnchor } from "./contextMenuKey.ts";

describe("isContextMenuKey", () => {
  it("matches the Menu key and Shift+F10 only", () => {
    expect(isContextMenuKey({ key: "ContextMenu", shiftKey: false })).toBe(true);
    expect(isContextMenuKey({ key: "F10", shiftKey: true })).toBe(true);
    expect(isContextMenuKey({ key: "F10", shiftKey: false })).toBe(false); // plain F10 is not it
    expect(isContextMenuKey({ key: "Enter", shiftKey: true })).toBe(false);
  });
});

describe("menuAnchor", () => {
  it("anchors just inside the leading edge, near the row's bottom", () => {
    const el = { getBoundingClientRect: () => ({ left: 100, bottom: 200, width: 300, height: 24 }) };
    // width/2 = 150, clamped to 24 → x = 124; y = bottom - 4 = 196.
    expect(menuAnchor(el as unknown as Element)).toEqual({ x: 124, y: 196 });
  });

  it("uses half the width for a narrow row (below the 24px clamp)", () => {
    const el = { getBoundingClientRect: () => ({ left: 10, bottom: 50, width: 30, height: 20 }) };
    expect(menuAnchor(el as unknown as Element)).toEqual({ x: 25, y: 46 }); // 10 + min(24,15)=15
  });
});
