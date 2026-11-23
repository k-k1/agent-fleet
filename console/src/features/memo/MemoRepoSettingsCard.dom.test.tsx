// The repository settings card (#4881): the check shows the difference with every row ticked,
// raised limits marked, the firing settings as one row; the approval sends the ticked keys with the
// commit and settings tag the difference showed; a stale answer reads the difference again.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({
  api: async () => ({}),
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  errText: (e: { message?: string } | undefined) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const commit = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff";
let settings: unknown;
const memoRepoSettings = vi.fn(async (_id: string) => settings);
let approveAnswer: unknown = { categoryId: "cat1" };
const memoRepoApprove = vi.fn(async (_id: string, _body: unknown) => approveAnswer);
const memoRepoExport = vi.fn(async (_id: string): Promise<unknown> => ({
  path: ".agent-fleet/autorun.yaml",
  category: "rounds",
  yaml: "version: 1\ncategories:\n  rounds:\n    batch: 2\n",
  omitted: [],
}));
vi.mock("./api.ts", () => ({
  memoRepoSettings: (id: string) => memoRepoSettings(id),
  memoRepoApprove: (id: string, body: unknown) => memoRepoApprove(id, body),
  memoRepoExport: (id: string) => memoRepoExport(id),
}));

const { MemoRepoSettingsCard } = await import("./MemoRepoSettingsCard.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
let applied = 0;
let busy: boolean[] = [];

const answer = (over: Record<string, unknown> = {}) => ({
  categoryId: "cat1",
  repo: "app",
  category: "rounds",
  path: ".agent-fleet/autorun.yaml",
  branch: "main",
  commit,
  found: true,
  entry: true,
  queue: true,
  approvalTag: "tag1",
  approvedCommit: "",
  approvedAt: "",
  errors: [],
  warnings: [{ line: 9, key: "enabled", reason: "turning auto-run on is set in the Console only" }],
  diffs: [
    { key: "spend_cap_usd", field: "spendCapUsd", line: 4, current: 60, file: 30, raise: false },
    { key: "max_active", field: "maxActive", line: 5, current: 2, file: 3, raise: true },
    { key: "open_cron", field: "openCron", line: 6, current: "0 9 * * *", file: "", raise: false },
    { key: "open_after", field: "openAfter", line: 7, current: 0, file: 5, raise: false },
  ],
  ...over,
});

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

async function render(props: { hasQueue?: boolean; blocked?: boolean; dirty?: boolean } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <MemoRepoSettingsCard
          categoryId="cat1"
          categoryName="rounds"
          hasQueue={props.hasQueue ?? true}
          blocked={props.blocked ?? false}
          dirty={props.dirty ?? false}
          onBusy={(b) => busy.push(b)}
          onApplied={() => applied++}
        />
      </ToastProvider>,
    );
  });
  await flush();
}

const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("button")];
const click = async (el: HTMLElement) => {
  await act(async () => el.click());
  await flush();
};
const rowsOf = () => [...host.querySelectorAll<HTMLLIElement>(".memo-repo-diff")];
const approveButton = () => host.querySelector<HTMLButtonElement>(".memo-repo-card .ui-btn-primary")!;

beforeEach(() => {
  settings = answer();
  approveAnswer = { categoryId: "cat1" };
  applied = 0;
  busy = [];
  memoRepoSettings.mockClear();
  memoRepoApprove.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("MemoRepoSettingsCard", () => {
  it("reads nothing until asked", async () => {
    await render();
    expect(memoRepoSettings).not.toHaveBeenCalled();
    expect(host.querySelector(".memo-repo-card")).toBeNull();
  });

  it("shows the difference and approves the ticked rows", async () => {
    await render();
    await click(buttons()[0]);
    expect(memoRepoSettings).toHaveBeenCalledWith("cat1");
    const rows = rowsOf();
    // The window and the threshold are one row.
    expect(rows).toHaveLength(3);
    expect(rows.map((r) => r.querySelector<HTMLInputElement>("input")!.checked)).toEqual([true, true, true]);
    expect(rows[1].hasAttribute("data-raise")).toBe(true);
    expect(rows[1].querySelector(".memo-repo-raise")).not.toBeNull();
    expect(rows[0].querySelector(".memo-repo-raise")).toBeNull();
    expect(host.querySelector(".memo-repo-warnings")!.textContent).toContain("enabled");

    // Untick the raised limit.
    await click(rows[1].querySelector<HTMLInputElement>("input")!);
    expect(approveButton().textContent).toMatch(/3/);
    await click(approveButton());
    expect(memoRepoApprove).toHaveBeenCalledWith("cat1", {
      commit,
      approvalTag: "tag1",
      keys: ["spend_cap_usd", "open_cron", "open_after"],
    });
    expect(applied).toBe(1);
    expect(busy).toEqual([true, false]);
  });

  it("ties each checkbox to the values it changes, for a screen reader", async () => {
    await render();
    await click(buttons()[0]);
    const box = rowsOf()[0].querySelector<HTMLInputElement>("input")!;
    const desc = document.getElementById(box.getAttribute("aria-describedby")!)!;
    expect(desc.textContent).toContain("60");
    expect(desc.textContent).toContain("30");
  });

  it("offers no approval while the dialog holds unsaved edits", async () => {
    await render({ dirty: true });
    await click(buttons()[0]);
    expect(approveButton().disabled).toBe(true);
    await click(approveButton());
    expect(memoRepoApprove).not.toHaveBeenCalled();
  });

  it("cannot read again while an approval is on its way, so a stale answer cannot race a manual read", async () => {
    let finish: (v: unknown) => void = () => {};
    memoRepoApprove.mockImplementationOnce(() => new Promise((r) => (finish = r)));
    await render();
    await click(buttons()[0]);
    await act(async () => approveButton().click());
    expect(buttons()[0].disabled).toBe(true);
    await act(async () => finish({ error: { code: "memo_queue_repo_stale", message: "changed" } }));
    await flush();
    expect(memoRepoSettings).toHaveBeenCalledTimes(2);
    expect(buttons()[0].disabled).toBe(false);
  });

  it("offers no approval with nothing ticked or with the tenant's switch off", async () => {
    await render({ blocked: true });
    await click(buttons()[0]);
    expect(approveButton().disabled).toBe(true);
    await render({ blocked: false });
    for (const r of rowsOf()) await click(r.querySelector<HTMLInputElement>("input")!);
    expect(approveButton().disabled).toBe(true);
  });

  it("reads the difference again when the approval was stale", async () => {
    approveAnswer = { error: { code: "memo_queue_repo_stale", message: "changed" } };
    await render();
    await click(buttons()[0]);
    await click(approveButton());
    expect(applied).toBe(0);
    expect(memoRepoSettings).toHaveBeenCalledTimes(2);
  });

  it("shows the file's errors and no approval", async () => {
    settings = answer({ errors: [{ line: 4, key: "max_active", reason: "maxActive must be 1 to 6" }] });
    await render();
    await click(buttons()[0]);
    expect(host.querySelector(".memo-repo-errors")!.textContent).toContain("maxActive must be 1 to 6");
    expect(host.querySelector(".memo-repo-card")).toBeNull();
  });

  it("says when there is no file, and names a read failure", async () => {
    settings = answer({ found: false, entry: false, diffs: [], warnings: [] });
    await render();
    await click(buttons()[0]);
    expect(host.querySelector(".memo-repo-card")).toBeNull();
    expect(host.textContent).toContain(".agent-fleet/autorun.yaml");

    settings = { error: { code: "workspace_not_running", message: "start your workspace" } };
    await click(buttons()[0]);
    expect(host.querySelector("[role=alert]")!.textContent).toContain("start your workspace");
  });

  it("exports the settings as YAML, only when there are settings", async () => {
    await render({ hasQueue: false });
    expect(buttons()).toHaveLength(1);
    await render({ hasQueue: true });
    await click(buttons()[1]);
    expect(memoRepoExport).toHaveBeenCalledWith("cat1");
    expect(host.querySelector<HTMLTextAreaElement>(".memo-repo-yaml")!.value).toContain("batch: 2");
  });
});
