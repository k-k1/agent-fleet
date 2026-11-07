// Wiring test for the terminal's clipboard keys: the key handler xterm calls must (a) copy and
// clear on Ctrl+C with a selection, (b) leave Ctrl+C alone with none, (c) paste once on Ctrl+V,
// and (d) toast when the clipboard refuses. Selection and clipboard are faked, so real-browser selection and OS clipboard behaviour are
// NOT covered here.
import { describe, it, expect, afterEach, vi } from "vitest";

const toasts: string[] = [];
vi.mock("../ui/toast.ts", () => ({ toast: (m: string) => toasts.push(String(m)), dismissToast: () => {} }));
// "yes" = Allow (once), "minute" = the explicit time-boxed choice, false = Cancel.
const confirmAnswers: (boolean | "minute")[] = [];
const confirms: any[] = [];
vi.mock("../ui/confirmBridge.ts", () => ({
  askConfirm: (o: any) => {
    confirms.push(o);
    const a = confirmAnswers.length ? confirmAnswers.shift() : false;
    if (a === "minute") o.onSecondary?.();
    return Promise.resolve(!!a);
  },
}));

import { ensureTerm, disposeTerm, OSC52_MAX_CHARS } from "./term.ts";
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
  confirms.length = 0;
  confirmAnswers.length = 0;
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

  for (const wait of [0, 250]) it(`a selection made while a keyboard copy's record timer is delayed is not absorbed as copied (Ctrl+C after ${wait} ms)`, async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText, readText: vi.fn() }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    await new Promise<void>((r) => term.write("drag line", r));
    term.select(0, 0, 4);
    const h = term._core._customKeyEventHandler;
    const real = globalThis.setTimeout;
    const spy = vi.spyOn(globalThis, "setTimeout").mockImplementation(((fn: any, ms?: number, ...a: any[]) =>
      real(fn, ms === 0 ? 100 : ms, ...a)) as any);
    h(key("KeyC", { ctrlKey: true, shiftKey: true })); // keyboard copy of "drag"
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    await new Promise((r) => real(r, 10));
    term.selectAll(); // a different selection while the record timer is pending
    spy.mockRestore();
    await new Promise((r) => real(r, wait)); // before, then after the record timer
    const copy = key("KeyC", { ctrlKey: true });
    expect(h(copy)).toBe(false);
    expect(copy.defaultPrevented).toBe(true);
    expect(writeText).toHaveBeenCalledTimes(2);
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

  const b64 = (t: string) => btoa(String.fromCharCode(...new TextEncoder().encode(t)));
  const oscOf = (term: any) => (t: string) => new Promise<void>((r) => term.write(`\x1b]52;c;${b64(t)}\x07`, r));

  it("OSC 52 without a member action never touches the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    const osc = oscOf(term);
    await osc("hello"); // prompt is denied (default answer)
    await settle();
    expect(confirms.length).toBe(1);
    expect(writeText).not.toHaveBeenCalled();
    // After a refusal, further writes neither write nor re-prompt.
    await osc("again");
    await settle();
    expect(confirms.length).toBe(1);
    expect(writeText).not.toHaveBeenCalled();
  });

  it("OSC 52 Allow covers that payload only; the next payload asks again", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    confirmAnswers.push(true);
    await oscOf(term)("hello");
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("hello"));
    expect(confirms[0].safeFocus).toBe(true);
    await oscOf(term)("second"); // asked again, default answer is Cancel
    await settle();
    expect(confirms.length).toBe(2);
    expect(writeText).not.toHaveBeenCalledWith("second");
  });

  it("OSC 52 explicit 'allow for a minute' is time-boxed and per pane", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    confirmAnswers.push("minute");
    await oscOf(term)("hello");
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("hello"));
    await oscOf(term)("second");
    expect(confirms.length).toBe(1);
    expect(writeText).toHaveBeenLastCalledWith("second");
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now + 61_000);
    await oscOf(term)("late");
    await settle();
    expect(confirms.length).toBe(2);
    expect(writeText).not.toHaveBeenCalledWith("late");
    vi.restoreAllMocks();
    disposeTerm("clip");
    const t2: any = mount();
    await oscOf(t2)("other pane");
    await settle();
    expect(confirms.length).toBe(3);
  });

  it("OSC 52 burst while a prompt is pending shows one prompt", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    confirmAnswers.push(true);
    const osc = oscOf(term);
    await Promise.all([osc("a"), osc("b"), osc("c")]);
    await settle();
    expect(confirms.length).toBe(1);
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  // Events bubble to document, like real ones: the gesture is completed by a document listener.
  const down = (el: Element, o: MouseEventInit = {}) =>
    el.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 10, clientY: 10, bubbles: true, ...o }));
  const up = (el: Element | Document, o: MouseEventInit = {}) =>
    el.dispatchEvent(new MouseEvent("mouseup", { button: 0, clientX: 80, clientY: 10, bubbles: true, ...o }));
  const drag = (term: any, moved = true, o: MouseEventInit = {}) => {
    down(term.element, o);
    up(term.element, { clientX: moved ? 80 : 10, ...o });
  };

  it("OSC 52 right after a drag-release is written without a prompt, once", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    const osc = oscOf(term);
    drag(term);
    await osc("dragged");
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("dragged"));
    expect(confirms.length).toBe(0);
    await osc("second"); // gesture was consumed
    await settle();
    expect(confirms.length).toBe(1);
    expect(writeText).not.toHaveBeenCalledWith("second");
  });

  it("OSC 52 without a gesture, after the window, or after a mere click still prompts", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    // Fresh pane per case: a refused prompt starts a cooldown that would mask the next one.
    const prompted = async (arrange: (term: any) => void) => {
      disposeTerm("clip");
      confirms.length = 0;
      const term: any = mount();
      arrange(term);
      await oscOf(term)("x");
      await settle();
      return confirms.length;
    };
    expect(await prompted(() => {})).toBe(1);
    expect(await prompted((t) => drag(t, false))).toBe(1); // click, no movement
    expect(
      await prompted((t) => {
        const now = Date.now();
        drag(t);
        vi.spyOn(Date, "now").mockReturnValue(now + 5000);
      }),
    ).toBe(1);
    expect(writeText).not.toHaveBeenCalled();
  });

  it("a shift-drag or a native selection leaves no prompt-free window", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    drag(term, true, { shiftKey: true });
    await oscOf(term)("shift");
    await settle();
    expect(confirms.length).toBe(1);
    disposeTerm("clip");
    const t2: any = mount();
    vi.spyOn(t2, "hasSelection").mockReturnValue(true);
    vi.spyOn(t2, "getSelection").mockReturnValue("mine");
    drag(t2);
    expect(writeText).toHaveBeenCalledWith("mine"); // the real selection copy still works
    await oscOf(t2)("attack");
    await settle();
    expect(confirms.length).toBe(2);
    expect(writeText).not.toHaveBeenCalledWith("attack");
  });

  it("a release outside the pane completes its own drag; another pane's press is not borrowed", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const a: any = mount();
    down(a.element);
    up(document.body); // released outside the pane
    await oscOf(a)("outside");
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("outside"));
    expect(confirms.length).toBe(0);
    // press in A, release outside (consumes A's press), press in B, release over A: no window for A.
    down(a.element);
    up(document.body);
    await oscOf(a)("consume");
    const el = document.createElement("div");
    document.body.appendChild(el);
    const b: any = ensureTerm("clip2", el);
    try {
      down(b.element);
      up(a.element);
      await oscOf(a)("stolen");
      await settle();
      expect(confirms.length).toBe(1);
      expect(writeText).not.toHaveBeenCalledWith("stolen");
    } finally {
      disposeTerm("clip2");
    }
  });

  it("OSC 52 text is stripped of control characters and capped", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const term: any = mount();
    confirmAnswers.push("minute");
    await oscOf(term)("a\x01b\x1bc\td\ne");
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("abc\td\ne"));
    await oscOf(term)("x".repeat(OSC52_MAX_CHARS + 50));
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(writeText.mock.calls[1][0].length).toBe(OSC52_MAX_CHARS);
  });

  it("selection copy is unaffected by OSC 52 consent", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    setSetting("termCtrlCV", true);
    const term: any = mount();
    userSelect(term);
    vi.spyOn(term, "hasSelection").mockReturnValue(true);
    vi.spyOn(term, "getSelection").mockReturnValue("sel");
    const h: KeyHandler = term._core._customKeyEventHandler;
    h(key("KeyC", { ctrlKey: true }));
    expect(writeText).toHaveBeenCalledWith("sel");
    expect(confirms.length).toBe(0);
  });

  it("OSC 52 copy toasts when the clipboard API is missing or refuses (after Allow)", async () => {
    const term: any = mount();
    confirmAnswers.push("minute");
    const osc = oscOf(term);
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    await osc("hi");
    await vi.waitFor(() => expect(toasts.length).toBe(1));
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn().mockRejectedValue(new Error("no")) }, configurable: true });
    await osc("hi");
    await vi.waitFor(() => expect(toasts.length).toBe(2));
    await new Promise<void>((r) => term.write("\x1b]52;c;?\x07", r));
    expect(toasts.length).toBe(2);
  });
});
