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
// Folder listing for the 作業ディレクトリ picker (api/fs/tree). Keyed by the browsed
// home-relative path so a click into a folder can serve that folder's children.
let tree: Record<string, string[]> = {};
const apiMock = vi.fn(async (url: string) => {
  if (url.includes("fs/tree")) {
    const path = decodeURIComponent(new URLSearchParams(url.split("?")[1]).get("path") || "");
    return { entries: (tree[path] || []).map((name) => ({ name, type: "dir" })) };
  }
  return { branches: served };
});

vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...(a as [string])),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

// A missing element means the UI moved (a control changed section / label), which is the
// thing these tests exist to catch — say so, instead of failing later on `undefined.click`.
function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}

const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const byText = (t: string) => must(buttons().find((b) => b.textContent?.includes(t)), `button "${t}"`);
// 場所 / 詳細 are collapsed sections (LaunchSection): their controls only exist in the
// DOM once the header is expanded. The header also carries the summary line, so match
// on the label span rather than the whole row.
const secHead = (label: string) =>
  must(
    buttons().find(
      (b) => b.classList.contains("launch-sec-head") && b.querySelector(".launch-sec-label")?.textContent === label,
    ),
    `section "${label}"`,
  );
const summaryOf = (label: string) => secHead(label).querySelector(".launch-sec-sum")!.textContent || "";
const expand = (label: string) => click(secHead(label));
const branchRows = () => [...document.querySelectorAll<HTMLButtonElement>(".branch-item")];
const rowFor = (name: string) => must(branchRows().find((b) => b.textContent?.includes(name)), `branch row "${name}"`);
// A folder row inside the 作業ディレクトリ browser.
const dirRow = (name: string) =>
  must([...document.querySelectorAll(".dirpick-row")].find((b) => b.textContent?.includes(name)), `folder row "${name}"`);

async function render(kinds = ["claude"]): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal repo="app" branch="main" kinds={kinds} onClose={() => {}} onLaunch={onLaunch} />,
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
  localStorage.clear();
  tree = { "repos/app": ["console", "workspace"], "repos/app/console": ["src"] };
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
  // The picker is a wrapping grid, not a scroller: every connected kind must be in the
  // DOM (the old horizontal scroller clipped the 4th card and hid the rest).
  it("renders every available agent as a card", async () => {
    const kinds = ["claude", "codex", "cursor", "copilot", "kiro", "opencode"];
    await render(kinds);
    expect(document.querySelectorAll(".ui-seg.big .seg-btn")).toHaveLength(kinds.length);
  });

  // 場所 is collapsed by default, so the summary line is the ONLY thing telling the user
  // what git is about to do. It has to describe the pending launch, not a stale default.
  it("summarises the pending location while the section is collapsed", async () => {
    await render();
    expect(summaryOf("Location")).toContain("New worktree");
    expect(summaryOf("Location")).toContain("main"); // the base branch it will fork from

    await expand("Location");
    await click(byText("Directly in this copy"));
    await expand("Location"); // collapse again
    expect(summaryOf("Location")).toContain("In this copy"); // launch.sum.direct
  });

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
    await expand("Location");
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
    await expand("Location");
    await click(byText("Existing branch"));
    expect(byText("Start in a worktree").disabled).toBe(true);
    await click(rowFor("develop"));
    expect(byText("Start in a worktree").disabled).toBe(false);
  });

  it("refuses to target a branch another working copy holds", async () => {
    await render();
    await expand("Location");
    await click(byText("Existing branch"));
    expect(rowFor("busy").disabled).toBe(true);
    expect(rowFor("busy").textContent).toContain("app@busy");
    await click(rowFor("busy"));
    expect(byText("Start in a worktree").disabled).toBe(true); // nothing got picked
  });

  // 作業ディレクトリ（Meta.Subdir）: which folder INSIDE the working copy the agent
  // starts in. Getting it wrong means the agent runs in the wrong package of a monorepo,
  // which looks like a working launch until it edits the wrong files. It lives in 場所
  // (Location) — where the launch happens — not in 詳細.
  it("launches in the folder picked from the tree", async () => {
    await render();
    await expand("Location");
    await click(byText("Browse"));
    await click(dirRow("console"));
    await click(dirRow("src"));
    await click(byText("Start in a worktree"));
    expect(launchedWith().subdir).toBe("console/src");
  });

  it("defaults to the working copy root and remembers the last folder per repo", async () => {
    await render();
    await click(byText("Start in a worktree"));
    expect(launchedWith().subdir).toBe(""); // untouched => the repo root

    act(() => root?.unmount());
    localStorage.setItem("af.repo-subdir.app", "console");
    root = createRoot(host);
    await render();
    await expand("Location");
    const input = must(document.querySelector<HTMLInputElement>(".subdirpick-input"), "作業ディレクトリ input");
    expect(input.value).toBe("console");
  });

  it("offers to use the colliding branch when a LOCAL name is taken", async () => {
    onLaunch = vi
      .fn<Launch>()
      .mockResolvedValueOnce({ ok: false, conflict: "local" })
      .mockResolvedValueOnce({ ok: true });
    await render();
    await expand("Location");
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
