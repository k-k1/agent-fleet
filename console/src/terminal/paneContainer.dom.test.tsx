// Regression guard for a pane showing another session's screen after a tab switch - it looks
// as if opening the terminal attached you to a different session's tmux.
//
// In tabbed display one cell reuses one TerminalView, and the container div is not remounted
// when the selected tab changes. xterm's open() appends, so without a guard the previous tab's
// .xterm stays in the div and the new .xterm is stacked below it, leaving the old session
// (tmux status line and all) on screen. The header, the PTY and the keystrokes all belong to
// the new session, so the terminal you see is not the terminal you are connected to.
//
// jsdom has no layout, so "which one is visible" cannot be measured. Instead this checks the
// invariant on the cause side: exactly one .xterm per container, the selected pane's.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ensureTerm, disposeTerm } from "./term.ts";
import { TerminalView } from "../features/terminal/TerminalView.tsx";

const termOf = (el: HTMLElement) => Array.from(el.querySelectorAll(".xterm"));

describe("terminal container ownership", () => {
  afterEach(() => {
    for (const id of ["p1", "p2"]) disposeTerm(id);
  });

  it("hands one container to exactly one pane", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);

    const first = ensureTerm("p1", el);
    expect(termOf(el)).toEqual([first!.element]);

    // Another pane takes the same div (a tab switch). The previous tab's .xterm must be gone.
    const second = ensureTerm("p2", el);
    expect(termOf(el)).toEqual([second!.element]);
    expect(second).not.toBe(first);

    // Switching back returns the original instance, scrollback and PTY intact.
    expect(ensureTerm("p1", el)).toBe(first);
    expect(termOf(el)).toEqual([first!.element]);

    el.remove();
  });

  it("keeps a reused TerminalView container on the selected pane only", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    let root: Root | null = null;
    await act(async () => {
      root = createRoot(host);
      // session=null: no PTY is opened; only container ownership matters here.
      root.render(<TerminalView paneId="p1" session={null} />);
    });
    const container = host.querySelector(".term-body .terminal") as HTMLElement;
    expect(termOf(container)).toHaveLength(1);

    // A tab switch is a re-render where only paneId changes; TerminalView is not remounted.
    await act(async () => {
      root!.render(<TerminalView paneId="p2" session={null} />);
    });
    expect(host.querySelector(".term-body .terminal")).toBe(container); // still the same div
    expect(termOf(container)).toHaveLength(1);

    await act(async () => root!.unmount());
    host.remove();
  });
});
