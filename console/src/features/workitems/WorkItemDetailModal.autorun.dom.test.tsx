// The detail modal's "Add to auto-run" action (ADR 0061 decision 20: actions live in the detail,
// not on the rows): it opens the picker for THIS item, on the repository a launch would default to.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./api.ts", async (orig) => ({ ...(await orig<typeof import("./api.ts")>()), workItemDetail: async () => ({}) }));
const memoCreate = vi.fn(async (_i: unknown): Promise<Record<string, unknown>> => ({ id: "n" }));
vi.mock("../memo/api.ts", () => ({
  memoCategoryList: async () => [{ id: "c1", repo: "app", name: "runs", position: 0, createdAt: "", queue: true }],
  memoList: async () => [],
  memoQueueList: async () => ({ tenantEnabled: true, queues: [] }),
  memoCreate: (i: unknown) => memoCreate(i),
}));

const { WorkItemDetailModal } = await import("./WorkItemDetailModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type WorkItem = import("./read.ts").WorkItem;

let root: Root | null = null;
let host: HTMLDivElement;
const item: WorkItem = {
  id: "1",
  queryId: "q1",
  provider: "github",
  kind: "issue",
  key: "acme/app#12",
  title: "Crash",
  state: "open",
  url: "https://github.com/acme/app/issues/12",
  assignee: "",
  labels: [],
  labelColors: {},
  repo: "acme/app",
  updatedAt: "2026-11-01T00:00:00Z",
  checks: { state: "", total: 0, failed: 0, pending: 0 },
  mergeable: "",
};
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

beforeEach(() => {
  localStorage.clear();
  memoCreate.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  document.body.querySelectorAll(".ui-confirm-backdrop, .ui-modal-backdrop").forEach((e) => e.remove());
});

describe("Add to auto-run in the detail", () => {
  it("opens the picker for the item, on the parent of the worktree a launch would default to, and files a pointer", async () => {
    await act(async () => {
      root!.render(
        <ToastProvider>
          <WorkItemDetailModal
            item={item}
            repos={[{ name: "app" }, { name: "app@feat", worktree: true, parent: "app" }] as never}
            defaultRepo="app@feat"
            started={[]}
            onClose={() => {}}
            onPick={() => {}}
            onStartHub={() => {}}
            sessionRef={() => ({ state: "live", title: "" })}
            onOpenSession={() => {}}
            onReport={() => {}}
          />
        </ToastProvider>,
      );
    });
    await settle();
    const action = [...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].find((b) => b.textContent === t("memo.addrun_action"))!;
    expect(action).toBeDefined();
    expect(document.querySelector(".confirm")).toBeNull();
    await act(async () => action.click());
    await settle();
    const dlg = document.querySelector(".confirm")!;
    expect(dlg.textContent).toContain(t("memo.addrun_title"));
    expect(dlg.textContent).toContain("acme/app#12");
    const repoSel = dlg.querySelector("select") as HTMLSelectElement;
    expect(repoSel.value).toBe("app");
    const catSel = dlg.querySelectorAll("select")[1] as HTMLSelectElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(catSel, "c1");
      catSel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => dlg.querySelector<HTMLButtonElement>("button[data-autofocus]")!.click());
    await settle();
    expect(memoCreate).toHaveBeenCalledWith({
      kind: "text",
      repo: "app",
      category: "runs",
      body: "",
      reference: { provider: "github", key: "acme/app#12", url: item.url },
    });
  });
});
