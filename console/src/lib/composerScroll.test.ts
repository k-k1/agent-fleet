import { describe, it, expect } from "vitest";
import { scrollComposerViewport } from "./composerScroll.ts";

// A minimal scrollable-element stand-in (node env has no DOM). clientHeight 200,
// scrollHeight 1000 → max scrollTop 800.
const makeEl = (scrollTop = 400) =>
  ({ scrollTop, scrollHeight: 1000, clientHeight: 200 }) as unknown as HTMLElement;

const key = (k: string, mods: Partial<{ shift: boolean; ctrl: boolean; meta: boolean }> = {}) => {
  let prevented = false;
  return {
    ev: {
      key: k,
      shiftKey: !!mods.shift,
      ctrlKey: !!mods.ctrl,
      metaKey: !!mods.meta,
      preventDefault: () => {
        prevented = true;
      },
    },
    wasPrevented: () => prevented,
  };
};

describe("scrollComposerViewport", () => {
  it("Shift+↓/↑ nudges by a line step and consumes the event", () => {
    const el = makeEl(400);
    const down = key("ArrowDown", { shift: true });
    expect(scrollComposerViewport(down.ev, el)).toBe(true);
    expect(el.scrollTop).toBe(448);
    expect(down.wasPrevented()).toBe(true);

    const up = key("ArrowUp", { shift: true });
    expect(scrollComposerViewport(up.ev, el)).toBe(true);
    expect(el.scrollTop).toBe(400);
  });

  it("Ctrl/⌘+↑/↓ pages by ~viewport height", () => {
    const el = makeEl(400);
    expect(scrollComposerViewport(key("ArrowDown", { ctrl: true }).ev, el)).toBe(true);
    expect(el.scrollTop).toBe(552); // 400 + (200 - 48)
    expect(scrollComposerViewport(key("ArrowUp", { meta: true }).ev, el)).toBe(true);
    expect(el.scrollTop).toBe(400);
  });

  it("Ctrl/⌘+[ / ] page just like the arrows", () => {
    const el = makeEl(400);
    expect(scrollComposerViewport(key("]", { ctrl: true }).ev, el)).toBe(true);
    expect(el.scrollTop).toBe(552);
    expect(scrollComposerViewport(key("[", { ctrl: true }).ev, el)).toBe(true);
    expect(el.scrollTop).toBe(400);
  });

  it("clamps at the edges but still consumes the key", () => {
    const top = makeEl(0);
    const u = key("ArrowUp", { shift: true });
    expect(scrollComposerViewport(u.ev, top)).toBe(true);
    expect(top.scrollTop).toBe(0);
    expect(u.wasPrevented()).toBe(true); // consumed so it can't fall through to history recall

    const bottom = makeEl(800);
    expect(scrollComposerViewport(key("ArrowDown", { ctrl: true }).ev, bottom)).toBe(true);
    expect(bottom.scrollTop).toBe(800);
  });

  it("ignores plain / unmodified arrows and other keys", () => {
    const el = makeEl(400);
    expect(scrollComposerViewport(key("ArrowUp").ev, el)).toBe(false);
    expect(scrollComposerViewport(key("ArrowDown").ev, el)).toBe(false);
    expect(scrollComposerViewport(key("[", { shift: true }).ev, el)).toBe(false);
    expect(scrollComposerViewport(key("a", { ctrl: true }).ev, el)).toBe(false);
    expect(el.scrollTop).toBe(400); // untouched
  });

  it("no-ops on a null element", () => {
    expect(scrollComposerViewport(key("ArrowDown", { shift: true }).ev, null)).toBe(false);
  });
});
