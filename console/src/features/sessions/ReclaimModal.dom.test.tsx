// The confirm behind the low-disk notice: it must list exactly what it will delete, delete
// nothing before the confirm, and go through the existing worktree-output and tool-cache calls.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Call {
  url: string;
  method: string;
}
let calls: Call[] = [];
let toolCaches: unknown;
let extraDir = false; // a directory that appears after the list was drawn
let cacheDelete: (name: string) => { status: number; body: unknown };

const GiB = 2 ** 30;
const wt = (name: string, bytes: number, status = "candidate") => ({
  name,
  path: "/home/dev/repos/" + name,
  status,
  dirs: status === "candidate" ? [{ rel: "node_modules", bytes }] : [],
  bytes,
});

const fetchMock = vi.fn(async (url: string, opts?: RequestInit) => {
  const u = String(url);
  const method = String(opts?.method || "GET");
  calls.push({ url: u, method });
  let status = 200;
  let body: unknown = {};
  if (u.includes("api/cleanup/worktree-output") && method === "GET") {
    body = { dormant_days: 7, worktrees: [wt("a", 2 * GiB), wt("b", GiB, "empty"), wt("c", GiB)], measured_at: "x" };
  } else if (u.includes("api/cleanup/worktree-output")) {
    const name = new URL(u, "http://x").searchParams.get("worktree")!;
    body = wt(name, name === "a" ? 2 * GiB : GiB);
    if (extraDir && name === "a") (body as any).dirs.push({ rel: "target", bytes: GiB });
  } else if (u.includes("api/cleanup/tool-caches/") && method === "DELETE") {
    const r = cacheDelete(decodeURIComponent(u.split("/").pop()!));
    status = r.status;
    body = r.body;
  } else if (u.includes("api/cleanup/tool-caches")) {
    body = toolCaches;
  }
  return {
    ok: status < 400,
    status,
    statusText: "",
    headers: { get: () => null },
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response;
});
vi.stubGlobal("fetch", fetchMock);

const { ReclaimModal } = await import("./ReclaimModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");
const store = await import("../../core/store/homeReclaim.ts");

let host: HTMLDivElement;
let root: Root;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
const onClose = vi.fn();
const onChanged = vi.fn();
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const real = () => calls.filter((c) => (c.method === "POST" && !c.url.includes("dry_run=1")) || c.method === "DELETE");
const confirmBtn = () => document.body.querySelector<HTMLButtonElement>("[data-autofocus]")!;

beforeEach(async () => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("en");
  calls = [];
  extraDir = false;
  onClose.mockClear();
  onChanged.mockClear();
  store.__test.reset();
  toolCaches = {
    caches: [
      { name: "go-build", bytes: 4 * GiB, files: 1 },
      { name: "npm", bytes: 3 * GiB, files: 1, busy: [42] },
      { name: "gradle-caches", bytes: 5 * GiB, files: 1, skipped: "unreadable" },
      { name: "pip", bytes: 0, files: 0 },
    ],
  };
  cacheDelete = (name) => ({ status: 200, body: { bytes: name === "go-build" ? 4 * GiB : 0 } });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <ToastProvider>
        <ReclaimModal onClose={onClose} onChanged={onChanged} />
      </ToastProvider>,
    );
  });
  await flush();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("ReclaimModal", () => {
  it("lists exactly the removable items and deletes nothing on open", () => {
    const ids = [...document.body.querySelectorAll("[data-testid=reclaim-items] > li")].map((l) => l.getAttribute("data-id"));
    // a, c (candidates, from dry runs), go-build only: busy, skipped and empty rows are out.
    expect(ids).toEqual(["wt:a", "wt:c", "cache:go-build"]);
    expect(document.body.textContent).toContain("node_modules");
    expect(real()).toEqual([]);
    expect(confirmBtn().textContent).toContain("7.0 GB");
  });

  it("goes through the existing worktree and tool-cache calls on confirm, and spends the walk", async () => {
    store.publishWalk({ measured_at: new Date().toISOString(), categories: [], repos: [{ bytes: 1, regenerable: GiB }] });
    expect(store.reclaimEstimate()).toBe(GiB);
    await act(async () => confirmBtn().click());
    await flush();
    expect(real().map((c) => c.method + " " + decodeURIComponent(c.url).replace(/^.*cleanup\//, ""))).toEqual([
      "POST worktree-output?worktree=a",
      "POST worktree-output?worktree=c",
      "DELETE tool-caches/go-build",
    ]);
    expect(store.reclaimEstimate()).toBeNull();
    expect(onChanged).toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("leaves out what was unticked", async () => {
    const box = document.body.querySelector<HTMLInputElement>('[data-id="wt:a"] input')!;
    await act(async () => box.click());
    expect(confirmBtn().textContent).toContain("5.0 GB");
    await act(async () => confirmBtn().click());
    await flush();
    expect(real().some((c) => c.url.includes("worktree=a"))).toBe(false);
    expect(real()).toHaveLength(2);
  });

  it("does not delete when everything is unticked", async () => {
    for (const b of document.body.querySelectorAll<HTMLInputElement>("[data-testid=reclaim-items] input")) {
      await act(async () => b.click());
    }
    expect(confirmBtn().disabled).toBe(true);
    await act(async () => confirmBtn().click());
    expect(real()).toEqual([]);
  });

  it("re-reads the dry run before deleting and stops for a fresh look when the directories changed", async () => {
    extraDir = true;
    await act(async () => confirmBtn().click());
    await flush();
    expect(real()).toEqual([]);
    expect(onClose).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("The list changed");
    expect(document.body.textContent).toContain("target");
    // The second confirm deletes what is now on screen.
    extraDir = true;
    await act(async () => confirmBtn().click());
    await flush();
    expect(real().length).toBe(3);
  });
});
