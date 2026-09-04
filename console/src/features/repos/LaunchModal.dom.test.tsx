// Render tests for the start-work dialog's branch section — the part that decides what git
// actually does. New-branch mode forks a branch off a base; existing-branch mode checks an
// EXISTING branch out into the worktree instead (base=<branch>, no new branch,
// use_existing). Those three fields going out wrong is the difference between
// "start work on develop" and "silently fork a divergent develop".
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
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
// Folder listing for the working-directory picker (api/fs/tree). Keyed by the browsed
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
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
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
// Location / Advanced are collapsed sections (LaunchSection): their controls only exist in the
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
// A folder row inside the working-directory browser.
const dirRow = (name: string) =>
  must([...document.querySelectorAll(".dirpick-row")].find((b) => b.textContent?.includes(name)), `folder row "${name}"`);

async function render(kinds = ["claude"], extra: { repo?: string; initialPrompt?: string } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal
        repo={extra.repo ?? "app"}
        branch="main"
        kinds={kinds}
        initialPrompt={extra.initialPrompt}
        onClose={() => {}}
        onLaunch={onLaunch}
      />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// Close and reopen the dialog, the way ✕ / Esc does: the component unmounts, so anything
// that survives has to have been persisted.
async function reopen(extra: { repo?: string; initialPrompt?: string } = {}): Promise<void> {
  act(() => root?.unmount());
  root = createRoot(host);
  await render(["claude"], extra);
  await settle(); // the attachment draft comes back from IndexedDB asynchronously
}

const promptBox = () => must(document.querySelector<HTMLTextAreaElement>("textarea"), "first-prompt textarea");
// Attachment chips (images waiting to be uploaded).
const chips = () => [...document.querySelectorAll(".mirror-attach .ma-chip")];

// Paste an image from the clipboard. jsdom has no DataTransfer, so only the clipboardData the
// handler reads is put on the raw event (React reads it from the native event).
async function pasteImage(name: string): Promise<void> {
  const file = new File(["PNGBYTES"], name, { type: "image/png" });
  const ev = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "clipboardData", {
    value: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
  });
  await act(async () => {
    promptBox().dispatchEvent(ev);
  });
  await settle();
}

// Both IndexedDB and React finish their reads and writes a few microtasks later.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function type(text: string): Promise<void> {
  const el = promptBox();
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
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
  // The attachment draft lives in IndexedDB (lib/attachDraft). Start every test with a fresh
  // DB and a connection re-opened against it.
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
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

  // Location is collapsed by default, so the summary line is the ONLY thing telling the user
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

  // The working directory (Meta.Subdir): which folder INSIDE the working copy the agent
  // starts in. Getting it wrong means the agent runs in the wrong package of a monorepo,
  // which looks like a working launch until it edits the wrong files. It lives in Location —
  // where the launch happens — not in Advanced.
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
    const input = must(document.querySelector<HTMLInputElement>(".subdirpick-input"), "working-directory input");
    expect(input.value).toBe("console");
  });

  // The first-prompt draft (launchDraft) survives closing, is kept per repository, and is
  // dropped only on a successful launch. Get it backwards and either the text is gone after a
  // trip to check the location, or an already-launched text squats on the next launch.
  it("keeps the typed first prompt per repo when the dialog is closed", async () => {
    await render();
    await type("直して");
    await reopen();
    expect(promptBox().value).toBe("直して");

    await reopen({ repo: "other" }); // never leaks into another repository
    expect(promptBox().value).toBe("");
  });

  it("drops the draft once the session has started", async () => {
    await render();
    await type("直して");
    expect(localStorage.getItem("af.launch-prompt.app")).toBe("直して"); // present before it is dropped
    await click(byText("Start in a worktree"));
    expect(launchedWith().prompt).toBe("直して");
    expect(localStorage.getItem("af.launch-prompt.app")).toBe(null);
    await reopen();
    expect(promptBox().value).toBe("");
  });

  // Coming back from a failed launch, the typed text is still needed (fix the collision and
  // press again).
  it("keeps the draft when the launch did not happen", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, conflict: "local" }));
    await render();
    await type("直して");
    await click(byText("Start in a worktree"));
    expect(promptBox().value).toBe("直して");
    await reopen();
    expect(promptBox().value).toBe("直して");
  });

  // A seed from a handoff proposal, a memo send or a work item outranks an earlier draft: the
  // caller opened the box having decided to start from that text.
  it("prefers a seeded first prompt over the stored draft", async () => {
    await render();
    await type("書きかけ");
    await reopen({ initialPrompt: "引き継ぎの本文" });
    expect(promptBox().value).toBe("引き継ぎの本文");
  });

  // An attachment (a pasted image) has the same lifetime as the text (lib/attachDraft): it
  // survives closing and is dropped only on a successful launch. Without it, a trip to check
  // the location lost the pasted screenshot alone — and because the text survived, the loss was
  // easy to miss.
  it("keeps a pasted image per repo when the dialog is closed", async () => {
    await render();
    await pasteImage("shot.png");
    expect(chips()).toHaveLength(1);
    await reopen();
    expect(chips()).toHaveLength(1);
    expect(chips()[0].querySelector("img.ma-thumb")?.getAttribute("src")).toMatch(/^blob:/);

    await reopen({ repo: "other" }); // never leaks into another repository
    expect(chips()).toHaveLength(0);
  });

  it("hands the restored image to the launch, then forgets it", async () => {
    await render();
    await pasteImage("shot.png");
    await reopen(); // after a close and reopen, the launch still gets the same file
    await click(byText("Start in a worktree"));
    expect(launchedWith().images.map((f) => f.name)).toEqual(["shot.png"]);
    await reopen();
    expect(chips()).toHaveLength(0);
  });

  // Coming back from a failed launch, the attachment is needed as much as the text.
  it("keeps the pasted image when the launch did not happen", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, conflict: "local" }));
    await render();
    await pasteImage("shot.png");
    await click(byText("Start in a worktree"));
    expect(chips()).toHaveLength(1);
    await reopen();
    expect(chips()).toHaveLength(1);
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
