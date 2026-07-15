import { describe, it, expect } from "vitest";
import { codeToKey, eventToChord, formatChord, canonical, eventChordString, shouldIgnore } from "./chords.ts";

// Minimal KeyboardEvent stand-in: the chord functions only read these fields, so a
// plain object cast to KeyboardEvent is enough (node env, no DOM).
function ev(code: string, mods: Partial<KeyboardEvent> = {}): KeyboardEvent {
  return { code, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods } as KeyboardEvent;
}

describe("codeToKey", () => {
  it("normalizes letters, digits, F-keys, arrows, symbols", () => {
    expect(codeToKey("KeyK")).toBe("k");
    expect(codeToKey("Digit1")).toBe("1");
    expect(codeToKey("Numpad1")).toBe("1");
    expect(codeToKey("F6")).toBe("f6");
    expect(codeToKey("F12")).toBe("f12");
    expect(codeToKey("ArrowLeft")).toBe("arrowleft");
    expect(codeToKey("BracketLeft")).toBe("[");
    expect(codeToKey("Escape")).toBe("escape");
    expect(codeToKey("Enter")).toBe("enter");
    expect(codeToKey("NumpadEnter")).toBe("enter");
  });
});

describe("eventToChord", () => {
  it("treats Ctrl and Meta as the same 'mod'", () => {
    expect(eventToChord(ev("KeyK", { ctrlKey: true }))).toEqual({ mod: true, alt: false, shift: false, key: "k" });
    expect(eventToChord(ev("KeyK", { metaKey: true }))).toEqual({ mod: true, alt: false, shift: false, key: "k" });
  });
  it("keeps Shift as its own modifier (h vs H)", () => {
    expect(formatChord(eventToChord(ev("KeyH"))!)).toBe("h");
    expect(formatChord(eventToChord(ev("KeyH", { shiftKey: true }))!)).toBe("shift+h");
  });
  it("returns null for a modifier-only keydown", () => {
    expect(eventToChord(ev("ShiftLeft", { shiftKey: true }))).toBeNull();
    expect(eventToChord(ev("ControlRight", { ctrlKey: true }))).toBeNull();
    expect(eventToChord(ev("AltLeft", { altKey: true }))).toBeNull();
  });
  it("is immune to Alt/Shift mutating the produced character (macOS ⌥+1 = ¡)", () => {
    // On macOS ⌥+1 yields e.key "¡"; using e.code keeps the chord stable.
    expect(eventChordString(ev("Digit1", { altKey: true }))).toBe("alt+1");
  });
});

describe("formatChord / parseChord / canonical", () => {
  it("emits modifiers in a fixed order regardless of input order", () => {
    expect(canonical("shift+mod+k")).toBe("mod+shift+k");
    expect(canonical("Mod+Shift+K")).toBe("mod+shift+k");
    expect(canonical("⌘+K")).toBe("mod+k");
    expect(canonical("Alt+1")).toBe("alt+1");
    expect(canonical("ctrl+alt+shift+f6")).toBe("mod+alt+shift+f6");
  });
  it("round-trips a live event through the same canonical form as its binding", () => {
    expect(eventChordString(ev("KeyK", { ctrlKey: true }))).toBe(canonical("mod+k"));
    expect(eventChordString(ev("KeyP", { metaKey: true }))).toBe(canonical("Cmd+P"));
  });
});

describe("shouldIgnore", () => {
  it("ignores IME composition and auto-repeat", () => {
    expect(shouldIgnore(ev("KeyK", { isComposing: true } as Partial<KeyboardEvent>))).toBe(true);
    expect(shouldIgnore(ev("KeyK", { keyCode: 229 } as Partial<KeyboardEvent>))).toBe(true);
    expect(shouldIgnore(ev("KeyK", { repeat: true } as Partial<KeyboardEvent>))).toBe(true);
    expect(shouldIgnore(ev("KeyK"))).toBe(false);
  });
});
