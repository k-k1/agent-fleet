// Render tests for the branch list's worktree-occupancy rules. git allows a branch
// in ONE working copy at a time, so a row held by another worktree must never fall
// through to a checkout/select that git would reject — it either hands the user off
// to the copy that holds it, or is inert.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { BranchList, wtFolder } = await import("./BranchList.tsx");
import type { Branch } from "./BranchList.tsx";

let root: Root | null = null;
let host: HTMLDivElement;

const BRANCHES: Branch[] = [
  { name: "main", unix: 3, current: true },
  { name: "free", unix: 2 },
  { name: "taken", unix: 1, worktree_path: "/home/dev/repos/app@taken" },
];

const rows = () => [...host.querySelectorAll<HTMLButtonElement>(".branch-item")];
const rowFor = (name: string) => rows().find((b) => b.textContent?.includes(name))!;
const sideButtons = () => [...host.querySelectorAll<HTMLButtonElement>(".branch-side")];

async function render(props: Partial<Parameters<typeof BranchList>[0]> = {}): Promise<void> {
  await act(async () => {
    root!.render(<BranchList branches={BRANCHES} selected="main" onPick={props.onPick ?? (() => {})} disableActive {...props} />);
  });
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("BranchList worktree occupancy", () => {
  it("labels an occupied branch with the working copy that holds it", async () => {
    await render();
    expect(rowFor("taken").textContent).toContain("app@taken");
    expect(rowFor("free").textContent).not.toContain("app@taken");
  });

  it("makes an occupied row inert when there is nowhere to hand off to", async () => {
    const onPick = vi.fn();
    await render({ onPick });
    expect(rowFor("taken").disabled).toBe(true);
    await click(rowFor("taken"));
    expect(onPick).not.toHaveBeenCalled();
  });

  it("routes an occupied row to its holder instead of picking it", async () => {
    const onPick = vi.fn();
    const onOpenWorktree = vi.fn();
    await render({ onPick, onOpenWorktree });
    expect(rowFor("taken").disabled).toBe(false);
    await click(rowFor("taken"));
    expect(onOpenWorktree).toHaveBeenCalledWith("app@taken", expect.objectContaining({ name: "taken" }));
    expect(onPick).not.toHaveBeenCalled();
    // A free row still picks normally.
    await click(rowFor("free"));
    expect(onPick).toHaveBeenCalledWith("free");
  });

  it("offers the start-work shortcut only where a new working copy is possible", async () => {
    const onStartWork = vi.fn();
    await render({ onStartWork });
    // Not on the current branch (already here) and not on an occupied one (git refuses).
    expect(sideButtons()).toHaveLength(1);
    await click(sideButtons()[0]);
    expect(onStartWork).toHaveBeenCalledWith("free");
  });
});

describe("wtFolder", () => {
  it("reduces a working-copy path to the folder name the Console shows", () => {
    expect(wtFolder("/home/dev/repos/app@wip-x")).toBe("app@wip-x");
    expect(wtFolder("/home/dev/repos/app@wip-x/")).toBe("app@wip-x");
    expect(wtFolder("")).toBe("");
    expect(wtFolder(undefined)).toBe("");
  });
});
