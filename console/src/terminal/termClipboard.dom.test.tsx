// Wiring test for the terminal's clipboard keys: the key handler xterm calls must (a) copy and
// clear on Ctrl+C with a selection, (b) leave Ctrl+C alone with none, (c) paste once on Ctrl+V,
// and (d) toast when the clipboard refuses. Selection and clipboard are faked, so real-browser selection and OS clipboard behaviour are
// NOT covered here.
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

// A user selection: xterm fires onSelectionChange, which is what Ctrl+C trusts. NB this fires
// BEFORE any mouseup; a real mouse selection is confirmed by xterm's document mouseup listener,
// after term.element's own mouseup (copy-on-select) has run. The "real event order" test below
// reproduces that order; tests using userSelect() alone would hide an ordering bug.
let selLen = 0; // xterm fires only when the range changes, so grow it each time
const userSelect = (term: any) => term.select(0, 0, ++selLen);

// The copy is recorded one timer tick after the write resolves (see copySelection).
const settle = () => new Promise<void>((r) => setTimeout(r, 10));

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
    userSelect(term);
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

  it("Ctrl+C after copy-on-select (same selection) reaches the PTY; a new selection copies", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    userSelect(term);
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    const getSel = vi.spyOn(term, "getSelection").mockReturnValue("drag");
    const h: KeyHandler = term._core._customKeyEventHandler;
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0 }));
    expect(writeText).toHaveBeenCalledWith("drag");
    await settle(); // let the write settle: a success is what marks it copied
    const intr = key("KeyC", { ctrlKey: true });
    expect(h(intr)).toBe(true);
    expect(intr.defaultPrevented).toBe(false);
    getSel.mockReturnValue("other");
    userSelect(term);
    expect(h(key("KeyC", { ctrlKey: true }))).toBe(false);
    expect(writeText).toHaveBeenLastCalledWith("other");
  });

  it("Ctrl+C is an interrupt when the program rewrites the copied selection's text", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    userSelect(term);
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    const getSel = vi.spyOn(term, "getSelection").mockReturnValue("progress 10%");
    const h: KeyHandler = term._core._customKeyEventHandler;
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0 }));
    await settle();
    getSel.mockReturnValue("progress 11%"); // same cells, redrawn: no selection event
    const intr = key("KeyC", { ctrlKey: true });
    expect(h(intr)).toBe(true);
    expect(intr.defaultPrevented).toBe(false);
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  for (const [name, end] of [["drag", 7], ["double-click", 3]] as const) {
    it(`Ctrl+C after a ${name} selection finalised by a document mouseup (real event order) is ^C`, async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
      setSetting("termCtrlCV", true);
      const term: any = mount();
      await new Promise<void>((r) => term.write("drag line", r));
      // Mid-drag xterm already holds the range in its model but has not fired
      // onSelectionChange; its document-level mouseup listener does that afterwards.
      const svc = term._core._selectionService;
      svc._model.selectionStart = [0, 0];
      svc._model.selectionStartLength = end;
      expect(term.hasSelection()).toBe(true);
      const confirm = () => svc._fireEventIfSelectionChanged();
      document.addEventListener("mouseup", confirm);
      try {
        term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0, bubbles: true }));
      } finally {
        document.removeEventListener("mouseup", confirm);
      }
      expect(term.hasSelection()).toBe(true);
      await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
      await settle();
      const h: KeyHandler = term._core._customKeyEventHandler;
      const intr = key("KeyC", { ctrlKey: true });
      expect(h(intr)).toBe(true);
      expect(intr.defaultPrevented).toBe(false);
      expect(writeText).toHaveBeenCalledTimes(1);
    });
  }

  it("Ctrl+C is ^C after the copied selection's rows scroll (scrollback trim) while the write is pending", async () => {
    let resolveWrite!: () => void;
    const writeText = vi.fn().mockReturnValue(new Promise<void>((r) => (resolveWrite = r)));
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    term.resize(20, 2);
    term.options.scrollback = 1;
    await new Promise<void>((r) => term.write("before\r\nselected", r));
    term.select(0, 1, 8); // row 1: "selected"
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0, bubbles: true }));
    const before = JSON.stringify(term.getSelectionPosition());
    await new Promise<void>((r) => term.write("\r\nnext\r\nmore", r)); // trims scrollback, moves the rows
    resolveWrite();
    await settle();
    expect(term.hasSelection()).toBe(true);
    expect(JSON.stringify(term.getSelectionPosition())).not.toBe(before); // rows really moved
    const intr = key("KeyC", { ctrlKey: true });
    expect(term._core._customKeyEventHandler(intr)).toBe(true);
    expect(intr.defaultPrevented).toBe(false);
  });

  it("a different selection made over the same cells while the write is pending is still copyable", async () => {
    let resolveWrite!: () => void;
    const writeText = vi
      .fn()
      .mockReturnValueOnce(new Promise<void>((r) => (resolveWrite = r)))
      .mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    await new Promise<void>((r) => term.write("same cells", r));
    userSelect(term);
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0, bubbles: true }));
    await new Promise((r) => setTimeout(r, 0)); // end of the mouseup dispatch window
    term.clearSelection();
    await new Promise<void>((r) => term.write("\rSAME CELLS", r));
    term.select(0, 0, selLen); // same coordinates as the first selection
    resolveWrite();
    await settle();
    const copy = key("KeyC", { ctrlKey: true });
    expect(term._core._customKeyEventHandler(copy)).toBe(false);
    expect(copy.defaultPrevented).toBe(true);
  });

  it("a successful copy whose record timer is delayed still lets Ctrl+C through as ^C", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    await new Promise<void>((r) => term.write("drag line", r));
    userSelect(term);
    // Throttled background tab: zero-delay timers fire late.
    const real = globalThis.setTimeout;
    const spy = vi.spyOn(globalThis, "setTimeout").mockImplementation(((fn: any, ms?: number, ...a: any[]) =>
      real(fn, ms === 0 ? 150 : ms, ...a)) as any);
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0, bubbles: true }));
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    await new Promise((r) => real(r, 20)); // write resolved, record timer still pending
    const intr = key("KeyC", { ctrlKey: true });
    expect(term._core._customKeyEventHandler(intr)).toBe(true);
    expect(intr.defaultPrevented).toBe(false);
    spy.mockRestore();
    await new Promise((r) => real(r, 200));
    const again = key("KeyC", { ctrlKey: true });
    expect(term._core._customKeyEventHandler(again)).toBe(true); // recorded: still ^C
    userSelect(term); // a new selection is copyable again
    const copy = key("KeyC", { ctrlKey: true });
    expect(term._core._customKeyEventHandler(copy)).toBe(false);
  });

  it("a refused auto-copy leaves Ctrl+C free to retry the copy", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    userSelect(term);
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    vi.spyOn(term, "getSelection").mockReturnValue("drag");
    const h: KeyHandler = term._core._customKeyEventHandler;
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0 }));
    await vi.waitFor(() => expect(toasts.length).toBe(1));
    const retry = key("KeyC", { ctrlKey: true });
    expect(h(retry)).toBe(false);
    expect(retry.defaultPrevented).toBe(true);
    expect(writeText).toHaveBeenCalledTimes(2);
  });

  it("an earlier successful copy of the same text does not survive a later refused auto-copy", async () => {
    const writeText = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    userSelect(term);
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    vi.spyOn(term, "getSelection").mockReturnValue("drag");
    const h: KeyHandler = term._core._customKeyEventHandler;
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0 }));
    await settle();
    term.element.dispatchEvent(new MouseEvent("mouseup", { button: 0 }));
    await vi.waitFor(() => expect(toasts.length).toBe(1));
    const retry = key("KeyC", { ctrlKey: true });
    expect(h(retry)).toBe(false);
    expect(writeText).toHaveBeenCalledTimes(3);
  });

  it("OSC 52 copy toasts when the clipboard API is missing or refuses", async () => {
    const term: any = mount();
    const osc = (b64: string) => new Promise<void>((r) => term.write(`\x1b]52;c;${b64}\x07`, r));
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    await osc("aGk=");
    expect(toasts.length).toBe(1);
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn().mockRejectedValue(new Error("no")) }, configurable: true });
    await osc("aGk=");
    await vi.waitFor(() => expect(toasts.length).toBe(2));
    await osc("?");
    expect(toasts.length).toBe(2);
  });
});
