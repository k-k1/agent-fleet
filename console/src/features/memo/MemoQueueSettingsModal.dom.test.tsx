// The memo queue's agent picker lists only the kinds the credential policy allows and the member
// has not switched off (ADR 0138 D10a), like the schedule editor; a kind with nothing usable is out.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { MemoQueue } from "../../types/memo.ts";

let liveKinds: Record<string, unknown>[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: async (url: string) => (url.includes("agents/kinds") ? { kinds: liveKinds, access: { policyConfirmed: true } } : {}),
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));
const memoQueuePut = vi.fn(async (_id: string, _body: unknown) => ({}));
let repoAnswer: unknown = {};
let approveGate: Promise<unknown> = Promise.resolve({});
const memoRepoApprove = vi.fn((_id: string, _body: unknown) => approveGate);
vi.mock("./api.ts", () => ({
  memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body),
  memoQueueDelete: vi.fn(),
  memoRepoSettings: async () => repoAnswer,
  memoRepoApprove: (id: string, body: unknown) => memoRepoApprove(id, body),
  memoRepoExport: vi.fn(),
}));

const { MemoQueueSettingsModal } = await import("./MemoQueueSettingsModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useQueueSettingsCache } = await import("./queueSettingsCache.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(repo = "", onClose: () => void = () => {}, queue: MemoQueue | undefined = undefined): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoQueueSettingsModal categoryName="c" categoryId="cat1" repo={repo} queue={queue} tenantEnabled destinations={[]} onClose={onClose} onChanged={() => {}} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const acceptBox = () => {
  const label = [...document.querySelectorAll("label")].find((l) => l.textContent?.includes("承認なしで受け付ける") || l.textContent?.includes("without approval"));
  return label?.querySelector<HTMLInputElement>("input[type=checkbox]") ?? null;
};

const agentOptions = () => [...document.querySelectorAll<HTMLOptionElement>("#memo-q-agent option")].map((o) => o.value);

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("MemoQueueSettingsModal agent picker", () => {
  it("lists only allowed kinds with something usable", async () => {
    liveKinds = [{ kind: "claude", usableRoutes: ["anthropic-api"] }, { kind: "codex", usableRoutes: [] }, { kind: "agy" }];
    await render();
    const opts = agentOptions();
    expect(opts).toContain("agy");
    expect(opts).not.toContain("codex");
    expect(opts).not.toContain("cursor");
  });
});

describe("MemoQueueSettingsModal accept-from-own-sessions switch", () => {
  it("is off by default, carries the warning text, and goes into the PUT body when turned on", async () => {
    liveKinds = [{ kind: "claude", usableRoutes: ["anthropic-api"] }];
    memoQueuePut.mockClear();
    await render();
    const box = acceptBox();
    expect(box).not.toBeNull();
    expect(box!.checked).toBe(false);
    expect(box!.closest("label")!.textContent).toMatch(/なりすま|pass itself off/);
    const save = async () => {
      await act(async () => {
        document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      });
      for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    };
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("cat1", expect.objectContaining({ acceptOwnSessions: false }));
    await act(async () => {
      box!.click();
    });
    expect(acceptBox()!.checked).toBe(true);
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("cat1", expect.objectContaining({ acceptOwnSessions: true }));
  });
});

describe("MemoQueueSettingsModal repository settings card", () => {
  const flush = async () => {
    for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
  };
  const submit = async () => {
    await act(async () => {
      document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await flush();
  };
  const checkFile = async () => {
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-repo-head button")!.click());
    await flush();
  };
  const approveBtn = () => document.querySelector<HTMLButtonElement>(".memo-repo-card .ui-btn-primary")!;

  beforeEach(() => {
    liveKinds = [{ kind: "claude", usableRoutes: ["anthropic-api"] }];
    useQueueSettingsCache.getState().reset();
    useQueueSettingsCache.setState({ repoSettings: true });
    memoQueuePut.mockClear();
    memoRepoApprove.mockClear();
    repoAnswer = {
      categoryId: "cat1",
      commit: "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff",
      found: true,
      entry: true,
      approvalTag: "t1",
      errors: [],
      warnings: [],
      diffs: [{ key: "batch", field: "batch", line: 4, current: 1, file: 2, raise: false }],
    };
  });
  // The cache is shared by every test of the file: a test that leaves a category unsure would
  // open the next one's dialog on the "reading again" screen.
  afterEach(() => useQueueSettingsCache.getState().reset());

  it("is not shown for a category without a repository", async () => {
    await render("");
    expect(document.querySelector(".memo-repo")).toBeNull();
  });

  it("does not save the dialog while an approval is on its way", async () => {
    let finish: (v: unknown) => void = () => {};
    approveGate = new Promise((r) => (finish = r));
    let closed = 0;
    await render("app", () => closed++);
    await checkFile();
    await act(async () => approveBtn().click());
    expect(memoRepoApprove).toHaveBeenCalledTimes(1);
    await submit();
    expect(memoQueuePut).not.toHaveBeenCalled();
    await act(async () => finish({ categoryId: "cat1" }));
    await flush();
    expect(memoQueuePut).not.toHaveBeenCalled();
    expect(closed).toBe(1);
  });

  it("never saves the old draft after an approval whose answer was lost", async () => {
    approveGate = Promise.reject(new TypeError("network"));
    approveGate.catch(() => {});
    let closed = 0;
    await render("app", () => closed++);
    await checkFile();
    await act(async () => approveBtn().click());
    await flush();
    expect(closed).toBe(1);
    // The form is gone until the settings are read again, so nothing can be sent from it.
    expect(document.querySelector("form")).toBeNull();
    expect(memoQueuePut).not.toHaveBeenCalled();
  });

  it("marks the settings unsure after a lost answer, and opens again only from a row read after it", async () => {
    approveGate = Promise.reject(new TypeError("network"));
    approveGate.catch(() => {});
    const { readQueue } = await import("./queues.ts");
    const { queueScope } = await import("./queueSettingsCache.ts");
    const old = readQueue({ categoryId: "cat1", instruction: "x", spendCapUsd: 60 })!;
    useQueueSettingsCache.getState().reset();
    useQueueSettingsCache.getState().publish(queueScope(), useQueueSettingsCache.getState().begin(), { tenantEnabled: true, repoSettings: true, queues: [old] });
    await render("app", () => {}, old);
    await checkFile();
    await act(async () => approveBtn().click());
    await flush();
    expect(useQueueSettingsCache.getState().unsure).toEqual(["cat1"]);
    // Opened again, the way the sections do: the row comes from the shared cache, which still
    // holds the one from before the approval. No form, nothing to save.
    function Live() {
      const q = useQueueSettingsCache((st) => st.queues.find((x) => x.categoryId === "cat1"));
      return <MemoQueueSettingsModal categoryName="c" categoryId="cat1" repo="app" queue={q} tenantEnabled destinations={[]} onClose={() => {}} onChanged={() => {}} />;
    }
    await act(async () => root!.unmount());
    root = createRoot(host);
    await act(async () => root!.render(<ToastProvider><ConfirmProvider><Live /></ConfirmProvider></ToastProvider>));
    await flush();
    expect(document.querySelector("#memo-q-budget")).toBeNull();
    expect(document.querySelector("form")).toBeNull();
    // A read begun after the mark lands with the approved value: the form opens from it.
    const fresh = readQueue({ categoryId: "cat1", instruction: "x", spendCapUsd: 30 })!;
    await act(async () =>
      useQueueSettingsCache.getState().publish(queueScope(), useQueueSettingsCache.getState().begin(), { tenantEnabled: true, repoSettings: true, queues: [fresh] }),
    );
    await flush();
    expect(document.querySelector<HTMLInputElement>("#memo-q-budget")!.value).toBe("30");
  });

  it("asks to save or cancel edits before an approval, which would drop them", async () => {
    await render("app");
    await checkFile();
    expect(approveBtn().disabled).toBe(false);
    const box = acceptBox()!;
    await act(async () => box.click());
    expect(approveBtn().disabled).toBe(true);
    expect(document.querySelector(".memo-repo-foot-hint")!.hasAttribute("data-invalid")).toBe(true);
    await act(async () => box.click());
    expect(approveBtn().disabled).toBe(false);
  });
});
