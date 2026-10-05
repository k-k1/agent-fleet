// A tenant switch between two running workspaces fires no running edge and no files tick, so
// the tree has to reload on the tenant itself and drop what the other workspace showed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};
// Set by a test that wants to hold an answer back; return a promise to delay the listing.
let gate: ((path: string) => Promise<void> | undefined) | null = null;
let searchHits: string[] = [];
let uploadGate: (() => Promise<void>) | null = null;

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    const entries = served[p] || []; // read when asked, delivered when the gate opens
    await gate?.(p);
    return { entries };
  }),
  isTransientErr: () => false,
  uploadFiles: vi.fn(async () => (await uploadGate?.(), {})),
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ results: searchHits, truncated: false })),
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useFilesFilter } = await import("./filesFilter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const names = () => [...host.querySelectorAll(".fsrow .fs-name")].map((n) => n.textContent);

const sleep = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));

async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await Promise.resolve();
    });
}

beforeEach(async () => {
  gate = null;
  uploadGate = null;
  searchHits = [];
  useFilesFilter.getState().setQ("");
  useWorkspaceStore.setState({ state: "running" });
  useTenantStore.setState({ tenant: "alpha" });
  served = {
    repos: [{ name: "alpha-repo", type: "dir" }],
    "repos/alpha-repo": [
      { name: "a.txt", type: "file" },
      { name: "b.txt", type: "file" },
    ],
  };
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectFiles root="repos" markRepos searchable />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("FILES tree on a tenant switch", () => {
  it("replaces the listing and collapses what the other workspace had open", async () => {
    await act(async () => {
      host.querySelector<HTMLElement>(".fsrow")!.click();
    });
    await settle();
    expect(names()).toEqual(["alpha-repo", "a.txt", "b.txt"]);

    served = { repos: [{ name: "beta-repo", type: "dir" }] };
    await act(async () => {
      useTenantStore.setState({ tenant: "beta" });
    });
    await settle();
    expect(names()).toEqual(["beta-repo"]);
  });

  // Both workspaces list repos/shared; the old tenant's listing is still on the wire when the
  // switch happens and lands first. It must not take the cache slot the new listing needs.
  it("ignores a listing from the previous tenant that lands after the switch", async () => {
    const held: Array<() => void> = [];
    gate = (p) => (p === "repos/shared" ? new Promise<void>((r) => held.push(r)) : undefined);
    served = { repos: [{ name: "shared", type: "dir" }], "repos/shared": [{ name: "old-secret", type: "dir" }] };
    await act(async () => {
      useTenantStore.setState({ tenant: "alpha2" });
    });
    await settle();
    served = { repos: [{ name: "shared", type: "dir" }], "repos/shared": [{ name: "new-secret", type: "dir" }] };
    await act(async () => {
      useTenantStore.setState({ tenant: "beta" });
    });
    await settle();
    expect(held.length).toBeGreaterThanOrEqual(2);
    // Release in arrival order: the previous tenant's answer first.
    for (const r of held) {
      r();
    }
    await settle();
    expect(names()).toEqual(["shared/new-secret"]);
  });

  it("replaces a recursive search's hits and searches again under the new tenant", async () => {
    searchHits = ["repos/alpha-repo/secret.txt"];
    await act(async () => {
      useFilesFilter.getState().setQ("secret");
    });
    await sleep(300);
    expect(names()).toEqual(["secret.txt alpha-repo"]);

    searchHits = ["repos/beta-repo/new-secret.txt"];
    await act(async () => {
      useTenantStore.setState({ tenant: "beta" });
    });
    await settle(); // inside the debounce: the old hits are gone before the new ones arrive
    expect(names()).toEqual([]);
    await sleep(300);
    expect(names()).toEqual(["new-secret.txt beta-repo"]);
  });

  // The re-read after an upload is started by the old tenant and is still on the wire at the switch.
  it("ignores the re-read an upload started under the previous tenant", async () => {
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, "files", { value: [new File(["x"], "x.txt")], configurable: true });
    let release!: () => void;
    gate = (p) => (p === "repos" ? new Promise<void>((r) => (release = r)) : undefined);
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    gate = null;
    served = { repos: [{ name: "beta-repo", type: "dir" }] };
    await act(async () => {
      useTenantStore.setState({ tenant: "beta" });
    });
    await settle();
    expect(names()).toEqual(["beta-repo"]);
    await act(async () => {
      release();
    });
    await settle();
    expect(names()).toEqual(["beta-repo"]);
  });

  // The mutation itself is still on the wire at the switch: its continuation must not refresh or
  // select anything in the new tenant's workspace.
  it("ignores an upload that finishes after the switch", async () => {
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, "files", { value: [new File(["x"], "x.txt")], configurable: true });
    let release!: () => void;
    uploadGate = () => new Promise<void>((r) => (release = r));
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const asked: string[] = [];
    gate = (p) => void asked.push(p);
    served = { repos: [{ name: "beta-repo", type: "dir" }] };
    await act(async () => {
      useTenantStore.setState({ tenant: "beta" });
    });
    await settle();
    asked.length = 0;
    await act(async () => {
      release();
    });
    await settle();
    expect(asked).toEqual([]); // no re-read of the old tenant's directory under the new tenant
    expect(names()).toEqual(["beta-repo"]);
  });
});
