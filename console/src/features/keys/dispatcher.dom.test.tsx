// Checks that the everyday Alt accelerators survive the whole path — real event, chord
// normalisation, command execution — by throwing real KeyboardEvents at the real
// dispatcher (wireKeys' capture listener).
//
// commands.test.ts only covers invariants of the registry DATA (duplicates, reserved-chord
// collisions). It cannot tell whether `.code` normalises to the intended chord (Comma ->
// ",", PageDown -> "pagedown", the order of Shift), nor whether a closed `when` gate
// releases the key to the terminal instead of claiming it. Those are what matter in
// production, so they are pinned here.
//
// Only side effects on the layout store are exercised; commands that touch the network
// (new session and friends) are out of scope.
import { describe, expect, it, beforeEach, afterEach } from "vitest";
import { useLayoutStore } from "../../layout/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { getSettings, setSetting, defaultSetting } from "../../lib/settings.ts";
import { FONT_MIN, FONT_MAX } from "../../lib/viewFont.ts";
import type { Layout } from "../../layout/types.ts";
import { wireKeys } from "./dispatcher.ts";
import { effectiveCommands } from "./bindings.ts";
import { matchDirect } from "../../lib/keys/registry.ts";
import { buildContext } from "./dispatcher.ts";

const view = (id: string, session: string) => ({
  id,
  session,
  content: { kind: "terminal" as const, chat: false },
  wrap: null,
});

/** Tabs mode: cell g0 holds three tabs, g1 holds one. */
const tabsLayout = (): Layout => ({
  version: 3,
  mode: "tabs",
  cols: [
    {
      id: "c0",
      rowRatio: 0.5,
      cells: [
        { id: "g0", selectedViewId: "p0", views: [view("p0", "alpha"), view("p1", "beta"), view("p2", "gamma")] },
        { id: "g1", selectedViewId: "p9", views: [view("p9", "solo")] },
      ],
    },
  ],
  colRatios: [1],
  activeCellId: "g0",
});

/** Sends a real key to the capture listener. Returns whether it was preventDefault'd, i.e.
 *  whether the app claimed it. */
const press = (code: string, mods: { alt?: boolean; shift?: boolean; mod?: boolean } = {}): boolean => {
  const e = new KeyboardEvent("keydown", {
    code,
    key: code,
    altKey: !!mods.alt,
    shiftKey: !!mods.shift,
    ctrlKey: !!mods.mod,
    bubbles: true,
    cancelable: true,
  });
  window.dispatchEvent(e);
  return e.defaultPrevented;
};

const layout = () => useLayoutStore.getState().layout;
const viewIds = (): string[] => layout().cols.flatMap((c) => c.cells.flatMap((g) => g.views.map((v) => v.id)));
const selected = (cellId: string): string | null =>
  layout().cols[0].cells.find((c) => c.id === cellId)?.selectedViewId ?? null;

describe("Alt accelerators (real dispatcher, real KeyboardEvents)", () => {
  let unwire: (() => void) | null = null;
  beforeEach(() => {
    useLayoutStore.setState({ layout: tabsLayout() });
    unwire = wireKeys();
  });
  afterEach(() => {
    unwire?.();
    unwire = null;
  });

  it("Alt+W closes only the ACTIVE TAB — not the whole cell (the bug this fixes)", () => {
    expect(press("KeyW", { alt: true })).toBe(true);
    expect(viewIds()).toEqual(["p1", "p2", "p9"]);
    // A remaining tab becomes selected and the cell stays alive.
    expect(selected("g0")).toBe("p1");
  });

  it("Alt+Shift+W is a DIFFERENT chord and closes everything", () => {
    expect(press("KeyW", { alt: true, shift: true })).toBe(true);
    expect(viewIds()).toEqual([]);
    expect(layout().cols[0].cells).toHaveLength(1);
  });

  it("Alt+PageDown / Alt+PageUp cycle tabs inside the active cell, wrapping", () => {
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p1");
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p0"); // wrapped
    expect(press("PageUp", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p2");
  });

  it("leaves Alt+PageDown to the terminal when the active cell has nothing to cycle", () => {
    useLayoutStore.setState({ layout: { ...tabsLayout(), activeCellId: "g1" } });
    expect(press("PageDown", { alt: true })).toBe(false); // treated as unbound, so passed through
    expect(selected("g1")).toBe("p9");
  });

  it("Alt+= / Alt+- / Alt+0 drive the font size of the ACTIVE pane's surface", () => {
    // The active pane is the terminal (p0 in tabsLayout), so termSize moves, not viewerSize.
    setSetting("termSize", 13);
    setSetting("viewerSize", 13);
    expect(press("Equal", { alt: true })).toBe(true);
    expect(getSettings().termSize).toBe(14);
    expect(getSettings().viewerSize).toBe(13); // other surfaces are not dragged along
    // On a US layout "+" is Shift+=, and it must land on the same enlargement.
    expect(press("Equal", { alt: true, shift: true })).toBe(true);
    expect(getSettings().termSize).toBe(15);
    expect(press("Minus", { alt: true })).toBe(true);
    expect(getSettings().termSize).toBe(14);
    expect(press("Digit0", { alt: true })).toBe(true);
    expect(getSettings().termSize).toBe(defaultSetting("termSize"));
  });

  it("stops at the bounds instead of running away", () => {
    setSetting("termSize", FONT_MAX);
    press("Equal", { alt: true });
    expect(getSettings().termSize).toBe(FONT_MAX);
    setSetting("termSize", FONT_MIN);
    press("Minus", { alt: true });
    expect(getSettings().termSize).toBe(FONT_MIN);
    setSetting("termSize", 13);
  });

  it("leaves the font keys to the terminal on a pane with no text (browser)", () => {
    const l = tabsLayout();
    l.cols[0].cells[0].views[0].content = { kind: "browser", port: 5173, path: "/" };
    useLayoutStore.setState({ layout: l });
    setSetting("termSize", 13);
    expect(press("Equal", { alt: true })).toBe(false); // treated as unbound, so passed through
    expect(getSettings().termSize).toBe(13);
  });

  it("resolves the punctuation and letter accelerators to the intended commands", () => {
    // For commands whose run() reaches outside (settings, memo, read-aloud, ...) only the
    // match is checked, never the execution. The rail filter only renders while the
    // workspace is running, so that gate is checked here too.
    const ctxNow = { ...buildContext(), region: "main" as const, focusedKind: "other" as const };
    expect(matchDirect(effectiveCommands(), "alt+/", ctxNow)).toBeUndefined(); // not claimed while stopped
    useWorkspaceStore.setState({ state: "running" });

    const cases: [string, boolean, string][] = [
      ["Comma", false, "settings.open"],
      ["Slash", false, "rail.filter"],
      ["KeyN", false, "session.new"],
      ["KeyA", false, "memo.add"],
      ["KeyG", false, "workspace.workingSet"],
      ["KeyQ", false, "tts.toggle"],
      ["KeyZ", false, "viewer.wrap"],
      ["BracketLeft", false, "pane.prev"],
      ["BracketRight", false, "pane.next"],
    ];
    const ctx = { ...buildContext(), region: "main" as const, focusedKind: "other" as const };
    for (const [code, shift, id] of cases) {
      const chord = "alt+" + (shift ? "shift+" : "") + chordKey(code);
      const cmd = matchDirect(effectiveCommands(), chord, ctx);
      expect(`${chord} → ${cmd?.id ?? "(none)"}`).toBe(`${chord} → ${id}`);
    }
  });
});

// `.code` -> the base key of the canonical chord, the same rule as codeToKey in chords.ts.
// It is only used to build expectations here, so it is spelled out plainly on the test side.
function chordKey(code: string): string {
  if (code === "Comma") return ",";
  if (code === "Slash") return "/";
  if (code === "BracketLeft") return "[";
  if (code === "BracketRight") return "]";
  return code.replace(/^Key/, "").toLowerCase();
}
