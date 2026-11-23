// The count chip of a repository or space row (ADR 0142 decision 10): shown only when the place
// has shells, and a click asks the Terminal view for that place without folding the row it sits on.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TerminalChip } from "./TerminalChip.tsx";
import { useTerminalReveal } from "./store.ts";

let root: Root | null = null;
let host: HTMLDivElement;
beforeEach(() => {
  useTerminalReveal.setState({ key: null, n: 0 });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

describe("TerminalChip", () => {
  it("is absent for a place with no shell", () => {
    act(() => root!.render(<TerminalChip count={0} groupKey="shell:repo:app" />));
    expect(host.querySelector(".term-chip")).toBeNull();
  });

  it("shows the count and asks for the place's group, leaving the row's own click alone", () => {
    const rowClick = vi.fn();
    act(() =>
      root!.render(
        <div onClick={rowClick}>
          <TerminalChip count={3} groupKey="shell:repo:app" />
        </div>,
      ),
    );
    const chip = host.querySelector<HTMLButtonElement>(".term-chip")!;
    expect(chip.textContent).toBe("3");
    act(() => chip.click());
    expect(useTerminalReveal.getState()).toMatchObject({ key: "shell:repo:app", n: 1 });
    expect(rowClick).not.toHaveBeenCalled();
  });
});
