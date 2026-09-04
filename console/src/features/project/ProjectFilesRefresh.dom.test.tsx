// Auto-refresh of the tree (on the signal that a session went back to waiting for input, i.e.
// the files store's scoped tick), plus the background reload when a folder is reopened.
//
// Three things are pinned here:
//   1. On the signal, re-read only the OPEN directories under that working copy. Being lighter
//      than the refresh button, which re-reads everything, is the whole point, so a widened
//      scope defeats it.
//   2. A failed reload must not clear the rows. Writing an api failure back as an empty listing
//      breaks far worse than a stale view: the tree empties out at the end of every turn.
//   3. A folder collapsed and reopened shows the cache while it re-reads in the background.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};
let failing = new Set<string>(); // paths that return a 5xx (transient failure)
let calls: string[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    calls.push(p);
    if (failing.has(p)) return { error: { code: "http_502", status: 502 } };
    if (!(p in served)) return { error: { code: "not_dir", status: 404 } };
    return { entries: served[p] };
  }),
  // Same decision as the real one: only a 5xx is transient, a 4xx is terminal.
  isTransientErr: (d: unknown) => {
    const err = (d as { error?: { code?: string; status?: number } } | null)?.error;
    if (!err) return false;
    if (typeof err.status === "number" && err.status >= 500) return true;
    return typeof err.code === "string" && /^http_5\d\d$/.test(err.code);
  },
  uploadFiles: vi.fn(),
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ results: [], truncated: false })),
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectFiles root="repos" markRepos />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

// Advance until rendering settles. Only the block that uses fake timers (the highlight fading
// out) cannot advance rAF, so it swaps the implementation.
const settleFrames = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
  }
};
const settleTimers = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20);
    });
  }
};
let settleImpl: () => Promise<void> = settleFrames;
async function settle(): Promise<void> {
  await settleImpl();
}

const paths = () => [...host.querySelectorAll<HTMLLIElement>(".fsrow")].map((li) => li.dataset.path);
const row = (p: string) => host.querySelector<HTMLLIElement>(`li[data-path="${p}"]`);
const click = async (el: Element | null) => {
  await act(async () => (el as HTMLElement).click());
  await settle();
};

/** The "now waiting for input" signal, the same one wireFilesSessionRefresh fires. */
const turnEnded = async (prefix: string) => {
  await act(async () => {
    useFilesStore.getState().refreshUnder(prefix);
  });
  await settle();
};

beforeEach(async () => {
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false }, tick: 0, scoped: { prefix: "", n: 0 } });
  useReposStore.setState({
    repos: [
      { name: "agent-fleet", branch: "develop" },
      { name: "other", branch: "develop" },
    ],
  });
  served = {
    repos: [
      { name: "agent-fleet", type: "dir" },
      { name: "other", type: "dir" },
    ],
    "repos/agent-fleet": [
      { name: "docs", type: "dir" },
      { name: "README.md", type: "file" },
    ],
    "repos/agent-fleet/docs": [{ name: "a.md", type: "file" }],
    "repos/other": [{ name: "x.md", type: "file" }],
  };
  failing = new Set();
  calls = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("auto-refresh when a session goes back to waiting for input", () => {
  it("shows files added to and removed from an open directory", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    expect(paths()).toContain("repos/agent-fleet/README.md");

    // The agent added one file and deleted another during the turn.
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "NEW.md", type: "file" },
    ];
    await turnEnded("repos/agent-fleet");

    expect(paths()).toContain("repos/agent-fleet/NEW.md");
    expect(paths()).not.toContain("repos/agent-fleet/README.md");
  });

  it("re-reads only what is on screen within the scope, never another working copy", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await click(row("repos/other"));
    calls = [];
    await turnEnded("repos/agent-fleet");

    // The opened repos/agent-fleet, the docs visible inside it (the tree pre-reads it anyway,
    // because chain folding a/b/c needs its contents), and this tree's root, where an added or
    // removed working copy would show. Another open working copy is not fetched.
    expect(new Set(calls)).toEqual(new Set(["repos", "repos/agent-fleet", "repos/agent-fleet/docs"]));
    expect(calls).not.toContain("repos/other"); // out of scope
  });

  it("keeps the current rows when the reload fails with a 502", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    const before = paths();
    expect(before).toContain("repos/agent-fleet/README.md");

    // The transient failure the CP returns right after a WS restart; api carries no entries.
    failing = new Set(["repos", "repos/agent-fleet"]);
    await turnEnded("repos/agent-fleet");

    expect(paths()).toEqual(before);
  });

  it("does not reload on a signal for another working copy", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [{ name: "NEW.md", type: "file" }];
    calls = [];
    await turnEnded("repos/other");

    expect(calls).not.toContain("repos/agent-fleet");
    expect(paths()).toContain("repos/agent-fleet/README.md"); // untouched
  });
});

describe("reopening a folder", () => {
  it("reflects what changed while it was collapsed", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await click(row("repos/agent-fleet")); // collapse
    expect(paths()).not.toContain("repos/agent-fleet/README.md");

    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "LATER.md", type: "file" },
    ];
    await click(row("repos/agent-fleet")); // reopen

    expect(paths()).toContain("repos/agent-fleet/LATER.md");
  });
});

// Revalidation on returning to the tab/window. The only way to pick up turns that ended while
// away, sessions that carry no state (shell / SSM) and changes made outside Agent Fleet.
describe("returning to the tab", () => {
  const comeBack = async () => {
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await settle();
  };

  it("re-reads the directories that are on screen", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "AWAY.md", type: "file" },
    ];
    calls = [];
    await comeBack();

    expect(calls).toContain("repos/agent-fleet");
    expect(paths()).toContain("repos/agent-fleet/AWAY.md");
  });

  it("does not fire on every return (a minimum interval applies)", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await comeBack();
    calls = [];
    await comeBack(); // returning again straight away adds no round trip
    expect(calls).toEqual([]);
  });
});

// Highlighting of added rows. The information wanted is WHICH row was added, not that something
// was, and fading out after a few seconds is part of the spec: if it stayed, the next addition
// could no longer be told apart.
describe("highlighting of added rows", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    settleImpl = settleTimers;
  });
  afterEach(() => {
    settleImpl = settleFrames;
    vi.useRealTimers();
  });

  const classOf = (p: string) => row(p)?.className ?? "";

  it("applies only to added rows and fades after a few seconds", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    served["repos/agent-fleet"] = [
      { name: "docs", type: "dir" },
      { name: "NEW.md", type: "file" },
      { name: "README.md", type: "file" },
    ];
    await turnEnded("repos/agent-fleet");

    expect(classOf("repos/agent-fleet/NEW.md")).toContain("fs-new");
    expect(classOf("repos/agent-fleet/README.md")).not.toContain("fs-new"); // a pre-existing row

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(classOf("repos/agent-fleet/NEW.md")).not.toContain("fs-new");
  });

  it("does not highlight on the first load, where every row would count as added", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    expect([...host.querySelectorAll(".fsrow.fs-new")]).toHaveLength(0);
  });
});
