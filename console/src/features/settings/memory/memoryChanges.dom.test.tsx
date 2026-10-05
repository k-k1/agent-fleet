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

import { AgentMemorySection } from "./memoryChanges.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { getSettings, setSettings, settingsDefaults } from "../../../lib/settings.ts";
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
          <AgentMemorySection reload={0} onChanged={() => {}} />
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

describe("AgentMemorySection", () => {
  // ADR 0108: the sessions' switch sits at the top of this section (#1735). It defaults OFF (an
  // upgrade must not switch it on) and writes the ui-prefs key the Agent reads (uiprefs.AgentMemory).
  it("carries the Agent Fleet memory switch, off by default, saving agentMemory", async () => {
    setSettings(settingsDefaults());
    await mount();
    const row = Array.from(host!.querySelectorAll(".ds-row")).find(
      (r) => r.querySelector(".ds-label")?.textContent === t("mem.af_switch"),
    );
    expect(row).toBeTruthy();
    expect(getSettings().agentMemory).toBe(false);
    const [on, off] = Array.from(row!.querySelectorAll<HTMLButtonElement>(".seg-btn"));
    expect(off.className).toContain("active");
    await act(async () => on.click());
    expect(getSettings().agentMemory).toBe(true);
    setSettings(settingsDefaults());
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
});
