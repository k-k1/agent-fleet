// Wiring test for the terminal's clipboard keys: the key handler xterm calls must (a) copy and
// clear on Ctrl+C with a selection, (b) leave Ctrl+C alone with none, (c) paste once on Ctrl+V,
// and (d) toast when the clipboard refuses. Selection and clipboard are faked; a real browser
// is covered separately by hand.
import { describe, it, expect, afterEach, vi } from "vitest";

const toasts: string[] = [];
vi.mock("../ui/toast.ts", () => ({ toast: (m: string) => toasts.push(String(m)), dismissToast: () => {} }));

import { ensureTerm, disposeTerm } from "./term.ts";
import { setSetting } from "../lib/settings.ts";

type KeyHandler = (e: KeyboardEvent) => boolean;

function mount() {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const term: any = ensureTerm("clip", el);
  return term;
}

const key = (code: string, o: KeyboardEventInit = {}) =>
  new KeyboardEvent("keydown", { code, cancelable: true, ...o });

afterEach(() => {
  disposeTerm("clip");
  toasts.length = 0;
  vi.restoreAllMocks();
});

describe("terminal clipboard keys", () => {
  it("Ctrl+C copies and clears a selection, is SIGINT otherwise; Ctrl+V pastes once", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const readText = vi.fn().mockResolvedValue("hello");
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    const pasted = vi.spyOn(term, "paste").mockImplementation(() => {});
    const h: KeyHandler = (term as any)._core._customKeyEventHandler;
    expect(h).toBeTypeOf("function");

    vi.spyOn(term, "hasSelection").mockReturnValue(false);
    const plain = key("KeyC", { ctrlKey: true });
    expect(h(plain)).toBe(true);
    expect(plain.defaultPrevented).toBe(false);
    expect(writeText).not.toHaveBeenCalled();

    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    vi.spyOn(term, "getSelection").mockReturnValue("sel");
    const clear = vi.spyOn(term, "clearSelection");
    const copy = key("KeyC", { ctrlKey: true });
    expect(h(copy)).toBe(false);
    expect(copy.defaultPrevented).toBe(true);
    expect(writeText).toHaveBeenCalledWith("sel");
    expect(clear).toHaveBeenCalled();

    const v = key("KeyV", { ctrlKey: true });
    expect(h(v)).toBe(false);
    expect(v.defaultPrevented).toBe(true);
    await vi.waitFor(() => expect(pasted).toHaveBeenCalledTimes(1));
    expect(readText).toHaveBeenCalledTimes(1);
  });

  it("with the setting off Ctrl+C / Ctrl+V go to the PTY", () => {
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn(), readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", false);
    const term: any = mount();
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    const h: KeyHandler = term._core._customKeyEventHandler;
    expect(h(key("KeyC", { ctrlKey: true }))).toBe(true);
    expect(h(key("KeyV", { ctrlKey: true }))).toBe(true);
    setSetting("termCtrlCV", true);
  });

  it("toasts instead of throwing when the clipboard is refused or missing", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockRejectedValue(new Error("denied")), readText: vi.fn().mockRejectedValue(new Error("denied")) },
      configurable: true,
    });
    const term: any = mount();
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    vi.spyOn(term, "getSelection").mockReturnValue("sel");
    const h: KeyHandler = term._core._customKeyEventHandler;
    h(key("KeyC", { ctrlKey: true, shiftKey: true }));
    h(key("KeyV", { ctrlKey: true, shiftKey: true }));
    await vi.waitFor(() => expect(toasts.length).toBe(2));
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    toasts.length = 0;
    expect(() => h(key("KeyV", { ctrlKey: true, shiftKey: true }))).not.toThrow();
    expect(toasts.length).toBe(1);
  });
});
