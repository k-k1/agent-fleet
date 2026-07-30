// Render tests for 作業を始める's branch section — the part that decides what git
// actually does. 新規ブランチ forks a branch off a base; 既存ブランチ checks an
// EXISTING branch out into the worktree instead (base=<branch>, no new branch,
// use_existing). Those three fields going out wrong is the difference between
// "start work on develop" and "silently fork a divergent develop".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Branch {
  name: string;
  unix?: number;
  current?: boolean;
  worktree_path?: string;
}

let served: Branch[] = [];
const apiMock = vi.fn(async () => ({ branches: served }));

vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...(a as [])),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const byText = (t: string) => buttons().find((b) => b.textContent?.includes(t))!;
const branchRows = () => [...document.querySelectorAll<HTMLButtonElement>(".branch-item")];
const rowFor = (name: string) => branchRows().find((b) => b.textContent?.includes(name))!;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal repo="app" branch="main" kinds={["claude"]} onClose={() => {}} onLaunch={onLaunch} />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const launchedWith = (): LaunchOpts => onLaunch.mock.calls[0][0] as LaunchOpts;

beforeEach(() => {
  served = [
    { name: "main", unix: 3, current: true },
    { name: "develop", unix: 2 },
    { name: "busy", unix: 1, worktree_path: "/home/dev/repos/app@busy" },
  ];
  apiMock.mockClear();
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("LaunchModal branch mode", () => {
  it("defaults to forking a new branch and never sets use_existing", async () => {
    await render();
    await click(byText("Start in a worktree"));
    const o = launchedWith();
    expect(o.worktree).toBe(true);
    expect(o.base).toBe("main"); // the base branch field
    expect(o.newBranch).toBe(""); // empty => the server mints temp/<slug>
    expect(o.useExisting).toBe(false);
  });

  it("checks an existing branch out instead of forking one", async () => {
    await render();
    await click(byText("Existing branch"));
    await click(rowFor("develop"));
    await click(byText("Start in a worktree"));
    const o = launchedWith();
    expect(o.base).toBe("develop"); // the branch IS the start point
    expect(o.newBranch).toBe(""); // nothing is created
    expect(o.useExisting).toBe(true);
  });

  it("blocks the launch until a branch is picked", async () => {
    await render();
    await click(byText("Existing branch"));
    expect(byText("Start in a worktree").disabled).toBe(true);
    await click(rowFor("develop"));
    expect(byText("Start in a worktree").disabled).toBe(false);
  });

  it("refuses to target a branch another working copy holds", async () => {
    await render();
    await click(byText("Existing branch"));
    expect(rowFor("busy").disabled).toBe(true);
    expect(rowFor("busy").textContent).toContain("app@busy");
    await click(rowFor("busy"));
    expect(byText("Start in a worktree").disabled).toBe(true); // nothing got picked
  });

  it("offers to use the colliding branch when a LOCAL name is taken", async () => {
    onLaunch = vi
      .fn<Launch>()
      .mockResolvedValueOnce({ ok: false, conflict: "local" })
      .mockResolvedValueOnce({ ok: true });
    await render();
    const input = document.querySelector<HTMLInputElement>('input[placeholder*="temporary name"]')!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "develop");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byText("Start in a worktree"));
    // The fix button used to appear for remote collisions only, dead-ending local ones.
    const fix = byText("Work on that existing branch");
    expect(fix).toBeTruthy();
    await click(fix);
    const o = onLaunch.mock.calls[1][0] as LaunchOpts;
    expect(o.base).toBe("develop");
    expect(o.newBranch).toBe("");
    expect(o.useExisting).toBe(true);
  });
});
