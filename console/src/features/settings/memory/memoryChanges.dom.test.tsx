// The AF memory change list (ADR 0108 decision 8). Pinned here: who/what each row says, that
// only the newest change of a memory offers the way back, and the secret flow — a revert whose
// text trips the scan is sent again with ack only after the member confirms the masked findings.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { AgentChangesPanel } from "./memoryChanges.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const changes = [
  {
    commit: "c2c2c2c2c2c2",
    at: "2026-10-04T10:00:00Z",
    op: "update",
    scope: "project",
    project: { id: "app-1", root: "/r/app", vcs: "git", display: "app" },
    name: "build-cmd",
    authorKind: "codex",
    authorSession: "s1",
    latest: true,
    live: true,
    revertible: true,
  },
  {
    commit: "c1c1c1c1c1c1",
    at: "2026-10-04T09:00:00Z",
    op: "create",
    scope: "project",
    project: { id: "app-1", root: "/r/app", vcs: "git", display: "app" },
    name: "build-cmd",
    authorKind: "claude",
    authorSession: "s0",
    latest: false,
    live: false,
    revertible: true,
  },
];

const flush = () =>
  act(async () => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  });

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <AgentChangesPanel reload={0} onChanged={() => {}} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}

const buttons = () => Array.from(document.body.querySelectorAll<HTMLButtonElement>("button"));
const click = async (b: HTMLButtonElement | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => b!.click());
  await flush();
};
const confirmButton = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child");

beforeEach(() => {
  api.mockImplementation((path: string) =>
    Promise.resolve(path.startsWith("api/agents/memory/entries/changes") ? { changes } : { diff: "+line" }),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

describe("AgentChangesPanel", () => {
  it("loads older changes by cursor, back to the start of the history", async () => {
    const older = [{ ...changes[1], commit: "old1old1old1", name: "ancient" }];
    api.mockImplementation((path: string) => {
      if (path === "api/agents/memory/entries/changes?limit=100") return Promise.resolve({ changes, next: "c2c2c2c2c2c2" });
      if (path === "api/agents/memory/entries/changes?limit=100&before=c2c2c2c2c2c2")
        return Promise.resolve({ changes: older, withheld: 1 });
      return Promise.resolve({ diff: "+line" });
    });
    await mount();
    const more = () => buttons().find((b) => b.textContent === t("mem.af_changes_more"));
    expect(host!.querySelectorAll(".mem-list .mem-snap")).toHaveLength(2);
    await click(more());
    const rows = Array.from(host!.querySelectorAll(".mem-list .mem-snap"));
    expect(rows).toHaveLength(3);
    expect(rows[2].textContent).toContain("ancient");
    expect(more()).toBeUndefined();
    expect(host!.textContent).toContain(t("mem.af_withheld", { n: 1 }));
  });

  it("lists who changed what and offers the way back on the newest change only", async () => {
    await mount();
    const rows = Array.from(host!.querySelectorAll(".mem-list .mem-snap"));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("build-cmd");
    expect(rows[0].textContent).toContain("app");
    expect(rows[0].textContent).toContain("codex · s1");
    expect(host!.querySelectorAll(".mem-diff-head button")).toHaveLength(2); // revert + forget

    await click(rows[1] as HTMLButtonElement);
    expect(host!.querySelectorAll(".mem-diff-head button")).toHaveLength(0);
  });

  it("re-sends a revert with ack only after the member confirms the masked findings", async () => {
    apiJSON
      .mockResolvedValueOnce({
        error: { code: "memory_secret_detected", message: "x" },
        findings: [{ path: "memory", line: 3, rule: "aws-access-key-id", hint: "AKIA…(20)" }],
      })
      .mockResolvedValueOnce({ name: "build-cmd", revision: 3 });
    await mount();
    await click(host!.querySelector<HTMLButtonElement>(".mem-diff-head button")!);
    await click(confirmButton()!); // "revert this change?"
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0][0]).toBe("api/agents/memory/entries/revert?commit=c2c2c2c2c2c2");
    expect(apiJSON.mock.calls[0][2]).toEqual({ commit: "c2c2c2c2c2c2", forget: false, ack: false });

    expect(document.body.querySelector(".mem-findings")?.textContent).toContain("AKIA…(20)");
    await click(confirmButton()!); // "I checked, continue"
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON.mock.calls[1][2]).toEqual({ commit: "c2c2c2c2c2c2", forget: false, ack: true });
  });

  it("does not send the acknowledged request when the member cancels", async () => {
    apiJSON.mockResolvedValueOnce({
      error: { code: "memory_secret_detected", message: "x" },
      findings: [{ path: "memory", line: 1, rule: "github-token", hint: "ghp_…(40)" }],
    });
    await mount();
    await click(buttons().find((b) => b.closest(".mem-diff-head") && b === b.parentElement?.lastElementChild));
    await click(confirmButton()!); // "forget this memory?"
    expect(apiJSON.mock.calls[0][2]).toEqual({ commit: "c2c2c2c2c2c2", forget: true, ack: false });
    const cancel = document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:first-child");
    await click(cancel!);
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("shows masked findings instead of a diff the Agent withheld", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/entries/changes")
          ? { changes }
          : { diff: "", withheld: true, findings: [{ path: "diff", line: 4, rule: "github-token", hint: "ghp_…(40)" }] },
      ),
    );
    await mount();
    expect(api.mock.calls.some(([p]) => p === "api/agents/memory/entries/diff?commit=c2c2c2c2c2c2")).toBe(true);
    expect(host!.querySelector(".mem-findings")?.textContent).toContain("ghp_…(40)");
    expect(host!.querySelector(".mem-diff .diff")).toBeNull();
  });

  it("offers no revert on a change the history cannot undo, and says why", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/entries/changes")
          ? { changes: [{ ...changes[0], op: "forget", live: false, revertible: false }] }
          : { diff: "" },
      ),
    );
    await mount();
    expect(host!.querySelectorAll(".mem-diff-head button")).toHaveLength(0);
    expect(host!.querySelector(".mem-diff-head .muted")).toBeTruthy();
  });

  it("reports a failed request and re-reads the list instead of throwing", async () => {
    apiJSON.mockRejectedValueOnce(new TypeError("network down"));
    const unhandled = vi.fn();
    window.addEventListener("unhandledrejection", unhandled);
    await mount();
    const before = api.mock.calls.filter(([p]) => String(p).startsWith("api/agents/memory/entries/changes")).length;
    await click(host!.querySelector<HTMLButtonElement>(".mem-diff-head button")!);
    await click(confirmButton()!);
    await flush();
    const after = api.mock.calls.filter(([p]) => String(p).startsWith("api/agents/memory/entries/changes")).length;
    expect(after).toBeGreaterThan(before);
    expect(document.body.textContent).toMatch(/confirm|確認/);
    window.removeEventListener("unhandledrejection", unhandled);
    expect(unhandled).not.toHaveBeenCalled();
  });

  it("says how many changes were withheld instead of reporting an empty history", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path.startsWith("api/agents/memory/entries/changes") ? { changes: [], withheld: 2 } : { diff: "" }),
    );
    await mount();
    expect(host!.textContent).toMatch(/2/);
    expect(host!.querySelector(".mem-warn")).toBeTruthy();
    expect(host!.querySelector(".mem-list li")).toBeNull();
  });

  it("shows a failed read as an error, not as no changes", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/entries/changes")
          ? { error: { code: "memory_snapshot_failed", message: "boom" } }
          : { diff: "" },
      ),
    );
    await mount();
    expect(host!.querySelector(".mem-warn")).toBeTruthy();
    expect(host!.querySelector(".mem-list li")).toBeNull();
  });

  describe("undoing a review run", () => {
    const reviewed = [{ ...changes[0], authorKind: "review", authorSession: "s1", reviewRun: "aaaaaaaaaaaaaaaaaaaa" }, changes[1]];
    const withRun = () =>
      api.mockImplementation((path: string) =>
        Promise.resolve(path.startsWith("api/agents/memory/entries/changes") ? { changes: reviewed } : { diff: "+line" }),
      );

    it("offers it only on a change the review published, and asks first", async () => {
      withRun();
      await mount();
      const labels = () => Array.from(host!.querySelectorAll(".mem-diff-head button")).map((b) => b.textContent);
      expect(labels()).toHaveLength(3); // revert, forget, undo the run
      await click(host!.querySelectorAll<HTMLButtonElement>(".mem-list .mem-snap")[1]); // an ordinary change
      expect(host!.querySelectorAll(".mem-diff-head button")).toHaveLength(0);
      await click(host!.querySelectorAll<HTMLButtonElement>(".mem-list .mem-snap")[0]);
      apiJSON.mockResolvedValueOnce({ run: "aaaaaaaaaaaaaaaaaaaa", items: [] });
      await click(host!.querySelectorAll<HTMLButtonElement>(".mem-diff-head button")[2]);
      expect(apiJSON).not.toHaveBeenCalled(); // not before the confirmation
      await click(confirmButton()!);
      expect(apiJSON.mock.calls[0]).toEqual([
        "api/agents/memory-review/runs/revert?run=aaaaaaaaaaaaaaaaaaaa",
        "POST",
        { run: "aaaaaaaaaaaaaaaaaaaa" },
      ]);
    });

    it("reports what it put back and names the memories it left, with the reason", async () => {
      withRun();
      apiJSON.mockResolvedValueOnce({
        run: "aaaaaaaaaaaaaaaaaaaa",
        items: [
          { commit: "1", op: "create", scope: "user", name: "put-back", result: "reverted" },
          { commit: "2", op: "update", scope: "user", name: "SENTINEL-EDITED", result: "conflict", reason: "edited" },
          { commit: "3", op: "update", scope: "user", name: "SENTINEL-BLOCKED", result: "conflict", reason: "blocked" },
          { commit: "4", op: "forget", scope: "user", name: "again", result: "already" },
        ],
      });
      await mount();
      await click(host!.querySelectorAll<HTMLButtonElement>(".mem-diff-head button")[2]);
      await click(confirmButton()!);
      const report = host!.querySelector(".mem-run-report")!;
      expect(report).toBeTruthy();
      expect(report.querySelectorAll("li")).toHaveLength(2);
      expect(report.textContent).toContain("SENTINEL-EDITED");
      expect(report.textContent).toContain("SENTINEL-BLOCKED");
      expect(report.textContent).not.toContain("put-back");
      expect(report.querySelectorAll("li")[0].textContent).not.toBe(report.querySelectorAll("li")[1].textContent.replace("BLOCKED", "EDITED"));
      await click(report.querySelector("button") ?? undefined);
      expect(host!.querySelector(".mem-run-report")).toBeNull();
    });

    it("says a refused request and keeps no report", async () => {
      withRun();
      apiJSON.mockResolvedValueOnce({ error: { code: "bad_request", message: "run is not a run id" } });
      await mount();
      await click(host!.querySelectorAll<HTMLButtonElement>(".mem-diff-head button")[2]);
      await click(confirmButton()!);
      expect(host!.querySelector(".mem-run-report")).toBeNull();
    });
  });
});
