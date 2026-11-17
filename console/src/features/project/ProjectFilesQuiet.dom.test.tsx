// What the file tree reads on its own (#4426). The repos root lists every working copy, so
// the tree's own reads scale with ~/repos, and each one is Workspace file-system I/O.
//
//   1. The prefetch asks for each directory once, however often its effect re-runs while the
//      answers are out, and keeps at most four reads on the wire. Without both, one page load
//      sent 1,363 listings in five seconds for ~130 working copies and stalled every other
//      connection to the Console, terminal WebSockets included.
//   2. While the Files view is hidden, nothing reads: not the end of a turn, not a refresh
//      tick, not a tenant switch, not a search, not coming back to the window, not the
//      prefetch. Showing the view again reads what it owes, once, and a debt it cannot pay
//      yet (a search owns the rows) is kept for the next show.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};
let calls: string[] = [];
let inFlight = 0;
let maxInFlight = 0;
// Answer after a delay that differs per path, so answers arrive one by one and each one
// re-runs the prefetch effect while the others are still out.
let delayed = false;
let searches = 0;

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    calls.push(p);
    inFlight++;
    maxInFlight = Math.max(maxInFlight, inFlight);
    try {
      if (delayed) await new Promise((r) => setTimeout(r, 2 + (p.length % 5)));
      if (!(p in served)) return { error: { code: "not_dir", status: 404 } };
      return { entries: served[p] };
    } finally {
      inFlight--;
    }
  }),
  isTransientErr: (d: unknown) => {
    const err = (d as { error?: { code?: string; status?: number } } | null)?.error;
    return !!err && ((typeof err.status === "number" && err.status >= 500) || /^http_5\d\d$/.test(err.code || ""));
  },
  uploadFiles: vi.fn(),
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => {
    searches++;
    return { results: [], truncated: false };
  }),
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ViewShownContext } = await import("../../lib/viewShown.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");
const { useFilesFilter } = await import("./filesFilter.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(shown = true): Promise<void> {
  await act(async () => {
    root!.render(
      <ViewShownContext.Provider value={shown}>
        <ToastProvider>
          <ConfirmProvider>
            <ProjectFiles root="repos" markRepos searchable />
          </ConfirmProvider>
        </ToastProvider>
      </ViewShownContext.Provider>,
    );
  });
  await settle();
}

// Until no read is out and a few more frames pass without one.
async function settle(): Promise<void> {
  let quiet = 0;
  for (let i = 0; i < 400 && quiet < 4; i++) {
    const before = calls.length;
    await act(async () => {
      await new Promise((r) => setTimeout(r, 5));
    });
    quiet = inFlight === 0 && calls.length === before ? quiet + 1 : 0;
  }
}

/** Each path read, sorted, with repeats kept: a set would hide a second read of a path. */
const sorted = (xs: string[]) => [...xs].sort();
const row = (p: string) => host.querySelector<HTMLLIElement>(`li[data-path="${p}"]`);
const click = async (el: Element | null) => {
  await act(async () => (el as HTMLElement).click());
  await settle();
};
const turnEnded = async (prefix: string) => {
  await act(async () => {
    useFilesStore.getState().refreshUnder(prefix);
  });
  await settle();
};

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false }, tick: 0, scoped: { prefix: "", n: 0 } });
  useReposStore.setState({ repos: [] });
  useTenantStore.setState({ tenant: "alpha" });
  useFilesFilter.setState({ q: "" });
  searches = 0;
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
  calls = [];
  inFlight = 0;
  maxInFlight = 0;
  delayed = false;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("prefetch of the working copies", () => {
  it("asks for each directory once, at most four at a time", async () => {
    const names = Array.from({ length: 24 }, (_, i) => `wc-${String(i).padStart(2, "0")}${"x".repeat(i % 5)}`);
    served = { repos: names.map((name) => ({ name, type: "dir" })) };
    for (const n of names) served[`repos/${n}`] = [{ name: "README.md", type: "file" }];
    delayed = true;

    await render();

    const counts = new Map<string, number>();
    for (const c of calls) counts.set(c, (counts.get(c) ?? 0) + 1);
    expect([...counts.entries()].filter(([, n]) => n > 1)).toEqual([]);
    expect(new Set(calls)).toEqual(new Set(["repos", ...names.map((n) => `repos/${n}`)]));
    expect(maxInFlight).toBeLessThanOrEqual(4);
  });
});

describe("while the Files view is hidden", () => {
  it("reads nothing at the end of a turn, and only the owed working copy once shown", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await click(row("repos/other"));
    await render(false);
    calls = [];

    await turnEnded("repos/agent-fleet");
    expect(calls).toEqual([]);

    served["repos/agent-fleet"] = [{ name: "NEW.md", type: "file" }];
    await render(true);
    // The same scope a turn end reads when shown: the working copy's rows and the root, once each.
    expect(sorted(calls)).toEqual(["repos", "repos/agent-fleet", "repos/agent-fleet/docs"]);
    expect(row("repos/agent-fleet/NEW.md")).not.toBeNull();
  });

  it("reads nothing when shown again if no turn ended meanwhile", async () => {
    await render();
    await render(false);
    calls = [];
    await render(true);
    expect(calls).toEqual([]);
  });

  it("holds a refresh tick until the view is shown", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await render(false);
    calls = [];

    await act(async () => useFilesStore.getState().bump());
    await settle();
    expect(calls).toEqual([]);

    await render(true);
    expect(calls).toContain("repos");
    expect(calls).toContain("repos/agent-fleet");
  });

  it("owes a return to the window and pays it once when shown", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await render(false);
    calls = [];
    await act(async () => window.dispatchEvent(new Event("focus")));
    await act(async () => window.dispatchEvent(new Event("focus")));
    await settle();
    expect(calls).toEqual([]);

    await render(true);
    // Everything on screen and the root, as the return itself would have read.
    expect(sorted(calls)).toEqual(["repos", "repos/agent-fleet", "repos/agent-fleet/docs", "repos/other"]);
  });

  it("keeps a return the gap skipped owed for a later show", async () => {
    let now = 1_000_000;
    const clock = vi.spyOn(Date, "now").mockImplementation(() => now);
    try {
      await render();
      await click(row("repos/agent-fleet"));
      await turnEnded("repos/agent-fleet"); // a scoped read: it sets the gap
      await render(false);
      await act(async () => window.dispatchEvent(new Event("focus")));
      calls = [];

      await render(true); // within the gap: nothing is read yet...
      expect(calls).toEqual([]);

      now += 60_000;
      await render(false);
      await render(true); // ...and the return is paid at the next show
      expect(sorted(calls)).toEqual(["repos", "repos/agent-fleet", "repos/agent-fleet/docs", "repos/other"]);
    } finally {
      clock.mockRestore();
    }
  });

  it("reads each path once when a tick and a turn end are both owed", async () => {
    await render();
    await click(row("repos/agent-fleet"));
    await render(false);
    calls = [];
    await act(async () => useFilesStore.getState().bump());
    await turnEnded("repos/agent-fleet");
    expect(calls).toEqual([]);

    await render(true);
    const counts = new Map<string, number>();
    for (const c of calls) counts.set(c, (counts.get(c) ?? 0) + 1);
    expect([...counts.values()].every((n) => n === 1)).toBe(true);
    expect(calls).toContain("repos/agent-fleet");
  });

  it("holds a tenant switch, and its prefetch, until shown", async () => {
    await render();
    await render(false);
    calls = [];
    await act(async () => useTenantStore.setState({ tenant: "beta" }));
    await settle();
    expect(calls).toEqual([]);

    await render(true);
    expect(sorted(calls)).toEqual(["repos", "repos/agent-fleet", "repos/other"]);
  });

  it("holds the prefetch a background reveal would start until shown", async () => {
    await render();
    await render(false);
    calls = [];
    // A reveal that is a side effect (a clone landing) does not bring the view forward. It may
    // read the folders on the way to its target, but not prefetch what opening them shows.
    await act(async () => useFilesStore.getState().revealInFiles("repos/agent-fleet/README.md"));
    await settle();
    expect(calls).not.toContain("repos/agent-fleet/docs");

    await render(true);
    expect(calls.filter((c) => c === "repos/agent-fleet/docs")).toHaveLength(1);
  });

  it("runs no search while hidden, and keeps a debt a search blocks", async () => {
    await render();
    await act(async () => useFilesFilter.setState({ q: "readme" }));
    await settle();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 250)); // the search is debounced
    });
    const before = searches;
    expect(before).toBe(1);

    await render(false);
    await act(async () => useFilesStore.getState().bump());
    await act(async () => {
      await new Promise((r) => setTimeout(r, 250));
    });
    expect(searches).toBe(before);

    // A turn end owed while hidden cannot be paid while the search owns the rows...
    await turnEnded("repos/agent-fleet");
    calls = [];
    await render(true);
    await act(async () => useFilesFilter.setState({ q: "" }));
    await settle();
    expect(calls).not.toContain("repos/agent-fleet");
    // ...so it is still owed at the next show.
    await render(false);
    await render(true);
    expect(calls).toContain("repos/agent-fleet");
  });

});
