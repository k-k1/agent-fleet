import { describe, it, expect } from "vitest";
import { clipAction, type ClipKeyEvent } from "./clipKeys.ts";

const ev = (code: string, o: Partial<ClipKeyEvent> = {}): ClipKeyEvent => ({
  type: "keydown",
  code,
  ctrlKey: false,
  metaKey: false,
  shiftKey: false,
  altKey: false,
  ...o,
});

describe("clipAction", () => {
  it("Ctrl+C with a selection copies and clears it", () => {
    expect(clipAction(ev("KeyC", { ctrlKey: true }), true, true)).toBe("copyClear");
  });
  it("Ctrl+C without a selection stays SIGINT", () => {
    expect(clipAction(ev("KeyC", { ctrlKey: true }), false, true)).toBeNull();
  });
  it("Ctrl+C is left to the PTY when the setting is off", () => {
    expect(clipAction(ev("KeyC", { ctrlKey: true }), true, false)).toBeNull();
  });
  it("Ctrl+V pastes only with the setting on", () => {
    expect(clipAction(ev("KeyV", { ctrlKey: true }), false, true)).toBe("paste");
    expect(clipAction(ev("KeyV", { ctrlKey: true }), false, false)).toBeNull();
  });
  it("Ctrl+Alt+C / Ctrl+Alt+V are not clipboard keys", () => {
    expect(clipAction(ev("KeyC", { ctrlKey: true, altKey: true }), true, true)).toBeNull();
    expect(clipAction(ev("KeyV", { ctrlKey: true, altKey: true }), true, true)).toBeNull();
  });
  it("keeps the older bindings regardless of the setting", () => {
    expect(clipAction(ev("KeyC", { ctrlKey: true, shiftKey: true }), false, false)).toBe("copy");
    expect(clipAction(ev("KeyV", { ctrlKey: true, shiftKey: true }), false, false)).toBe("paste");
    expect(clipAction(ev("Insert", { ctrlKey: true }), true, false)).toBe("copy");
    expect(clipAction(ev("Insert", { shiftKey: true }), false, false)).toBe("paste");
    expect(clipAction(ev("KeyC", { metaKey: true }), true, false)).toBe("copy");
    expect(clipAction(ev("KeyC", { metaKey: true }), false, false)).toBeNull();
    expect(clipAction(ev("KeyV", { metaKey: true }), false, false)).toBe("paste");
  });
  it("never acts during IME composition", () => {
    expect(clipAction(ev("KeyV", { ctrlKey: true, isComposing: true }), true, true)).toBeNull();
    expect(clipAction(ev("KeyC", { ctrlKey: true, keyCode: 229 }), true, true)).toBeNull();
  });
  it("ignores keyup", () => {
    expect(clipAction(ev("KeyV", { ctrlKey: true, type: "keyup" }), true, true)).toBeNull();
  });
});
