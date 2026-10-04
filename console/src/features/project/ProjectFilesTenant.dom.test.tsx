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

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    return { entries: served[p] || [] };
  }),
  isTransientErr: () => false,
  uploadFiles: vi.fn(),
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ hits: [] })),
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const names = () => [...host.querySelectorAll(".fsrow .fs-name")].map((n) => n.textContent);

async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await Promise.resolve();
    });
}

beforeEach(async () => {
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
          <ProjectFiles root="repos" markRepos />
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
});
