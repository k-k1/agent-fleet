// The Agent memory switch's confirmation and status (#1972). Pinned here: the dialog says the
// preview's counts before anything is saved, a "no" saves nothing, a "yes" saves the switch and
// wakes the Agent (audited through the query), a stopped workspace is flipped with no Agent call,
// and the failing write-back offers retry and "switch native memory back on anyway".
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
let wsState = "running";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string }) => unknown) => sel({ state: wsState }),
}));

const { AgentMemorySwitch } = await import("./memoryChanges.tsx");
const { ConfirmProvider } = await import("../../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../../ui/ToastProvider.tsx");
const { getSettings, setSettings, settingsDefaults } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <AgentMemorySwitch />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}

const switchButtons = () => {
  const row = Array.from(host!.querySelectorAll(".ds-row")).find(
    (r) => r.querySelector(".ds-label")?.textContent === t("mem.af_switch"),
  );
  return Array.from(row!.querySelectorAll<HTMLButtonElement>(".seg-btn")); // [on, off]
};
const click = async (b: HTMLButtonElement | null | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => b!.click());
  await flush();
};
const dialog = () => document.body.querySelector<HTMLElement>(".ui-confirm-backdrop, .ui-confirm");
const confirmBtn = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child");
const cancelBtn = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:first-child");

const idle = { phase: "off", want: false, settled: true, nativeMemoryOff: false };

beforeEach(() => {
  wsState = "running";
  setSettings(settingsDefaults());
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
  setSettings(settingsDefaults());
});

describe("flipping the Agent memory switch", () => {
  it("says what the import will do, saves nothing on a no, and saves + wakes the Agent on a yes", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/auto-sync/preview")
          ? {
              to: "on",
              projects: [{ id: "a", counts: { new: 3 } }, { id: "b", counts: { new: 2, secret: 1, conflict: 1 } }],
              totals: { new: 5, secret: 1, conflict: 1 },
              apply: 5,
              unmapped: 2,
            }
          : idle,
      ),
    );
    apiJSON.mockResolvedValue({});
    await mount();
    await click(switchButtons()[0]);

    expect(api).toHaveBeenCalledWith("api/agents/memory/auto-sync/preview?to=on");
    const text = document.body.textContent ?? "";
    expect(text).toContain(t("mem.as_confirm_on_body", { n: 5, projects: 2 }));
    expect(text).toContain(t("mem.as_confirm_secret", { n: 1 }));
    expect(text).toContain(t("mem.as_confirm_conflict", { n: 1 }));
    expect(text).toContain(t("mem.as_unmapped", { n: 2 }));
    expect(getSettings().agentMemory).toBe(false);

    await click(cancelBtn());
    expect(getSettings().agentMemory).toBe(false);
    expect(apiJSON).not.toHaveBeenCalled();

    await click(switchButtons()[0]);
    await click(confirmBtn());
    expect(getSettings().agentMemory).toBe(true);
    // The query is what the audit ledger records (the body is never read there).
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/auto-sync?action=wake&to=on", "POST", { action: "wake" });
  });

  it("names Muse Code's diverged notes, unmapped folders and the personal notes that wait for a choice", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/auto-sync/preview")
          ? {
              to: "on",
              projects: [
                { id: "a", counts: { new: 1 } },
                { agent: "muse", id: "a", counts: { new: 2, diverged: 1 } },
              ],
              totals: { new: 3, diverged: 1 },
              apply: 3,
              museUnmapped: 4,
              musePersonal: 5,
            }
          : idle,
      ),
    );
    await mount();
    await click(switchButtons()[0]);
    const text = document.body.textContent ?? "";
    // One project holding both agents' notes is one project.
    expect(text).toContain(t("mem.as_confirm_on_body", { n: 3, projects: 1 }));
    expect(text).toContain(t("mem.as_confirm_muse_diverged", { n: 1 }));
    expect(text).toContain(t("mem.as_muse_unmapped", { n: 4 }));
    expect(text).toContain(t("mem.as_confirm_muse_personal", { n: 5 }));
  });

  it("says what the write-back will do when switching off", async () => {
    setSettings({ ...settingsDefaults(), agentMemory: true });
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/auto-sync/preview")
          ? { to: "off", projects: [{ id: "a", counts: { new: 4 } }], totals: { new: 4, update: 1, conflict: 2 }, apply: 5, noRoot: 1 }
          : { phase: "on", want: true, settled: true, nativeMemoryOff: true },
      ),
    );
    apiJSON.mockResolvedValue({});
    await mount();
    await click(switchButtons()[1]);
    const text = document.body.textContent ?? "";
    expect(text).toContain(t("mem.as_confirm_off_body", { n: 5, projects: 1 }));
    expect(text).toContain(t("mem.as_confirm_conflict_off", { n: 2 }));
    expect(text).toContain(t("mem.as_no_root", { n: 1 }));
    await click(confirmBtn());
    expect(getSettings().agentMemory).toBe(false);
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/auto-sync?action=wake&to=off", "POST", { action: "wake" });
  });

  it("flips with no Agent call while the workspace is stopped, and says why there are no counts", async () => {
    wsState = "stopped";
    await mount();
    await click(switchButtons()[0]);
    expect(document.body.textContent).toContain(t("mem.as_confirm_stopped"));
    await click(confirmBtn());
    expect(getSettings().agentMemory).toBe(true);
    expect(api).not.toHaveBeenCalled();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("still asks, without counts, when the preview cannot be read", async () => {
    api.mockResolvedValue({ error: { code: "x", message: "boom" } });
    await mount();
    await click(switchButtons()[0]);
    expect(dialog()).toBeTruthy();
    expect(document.body.textContent).toContain(t("mem.as_confirm_unknown"));
  });
});

describe("the run's status under the switch", () => {
  it("lists what a run left and says nothing about files it applied", async () => {
    api.mockResolvedValue({
      phase: "on",
      want: true,
      settled: true,
      nativeMemoryOff: true,
      catchUpPending: 2,
      run: {
        kind: "import",
        started: "2026-10-14T09:00:00Z",
        finished: "2026-10-14T09:00:05Z",
        complete: true,
        projects: [{ id: "a", counts: { imported: 4, updated: 1, unchanged: 7, conflict: 1, secret: 1 } }],
        unmapped: 1,
        rows: [
          { project: "a", name: "shared", status: "conflict", reason: "af_changed" },
          { project: "a", name: "leaky", status: "secret", rules: ["github-token"] },
        ],
      },
    });
    setSettings({ ...settingsDefaults(), agentMemory: true });
    await mount();
    const text = host!.textContent ?? "";
    expect(text).toContain(t("mem.as_phase_on"));
    expect(text).toContain(t("mem.as_last_import", { imported: 4, updated: 1, unchanged: 7 }));
    expect(text).toContain(t("mem.as_catchup_pending", { n: 2 }));
    expect(text).toContain(t("mem.as_rows", { n: 2 }));
    expect(text).toContain("shared");
    expect(text).toContain(t("mem.as_reason_af_changed"));
    expect(text).toContain("github-token");
  });

  it("lists Muse Code's per-item conflicts and the personal notes that need confirmation", async () => {
    api.mockResolvedValue({
      phase: "on",
      want: true,
      settled: true,
      nativeMemoryOff: true,
      run: {
        kind: "import",
        started: "2026-10-14T09:00:00Z",
        finished: "2026-10-14T09:00:05Z",
        complete: true,
        projects: [{ agent: "muse", id: "a", counts: { imported: 2 } }],
        museUnmapped: 1,
        museFailed: 2,
        musePersonal: 3,
        rows: [
          { agent: "muse", project: "a", name: "mine", status: "diverged", reason: "other_source" },
          { agent: "muse", project: "user", name: "personal", status: "needs_confirmation", reason: "choose_user_scope" },
        ],
      },
    });
    setSettings({ ...settingsDefaults(), agentMemory: true });
    await mount();
    const text = host!.textContent ?? "";
    expect(text).toContain(t("mem.as_last_import", { imported: 2, updated: 0, unchanged: 0 }));
    expect(text).toContain(t("mem.as_muse_unmapped", { n: 1 }));
    expect(text).toContain(t("mem.as_muse_failed", { n: 2 }));
    expect(text).toContain(t("mem.as_status_diverged"));
    expect(text).toContain(t("mem.as_reason_other_source"));
    expect(text).toContain(t("mem.as_muse_personal_name", { n: 3 }));
    expect(text).toContain(t("mem.as_status_needs_confirmation"));
  });

  it("offers retry and 'switch native memory back on anyway' once the write-back keeps failing", async () => {
    api.mockResolvedValue({
      phase: "writing_back",
      want: false,
      settled: false,
      failing: true,
      attempts: 3,
      nativeMemoryOff: true,
      run: { kind: "write_back", started: "2026-10-14T09:00:00Z", finished: "2026-10-14T09:00:01Z", complete: false, error: "memory_snapshot_failed", projects: [] },
    });
    apiJSON.mockResolvedValue({});
    await mount();
    expect(host!.textContent).toContain(t("mem.as_failing", { error: "memory_snapshot_failed" }));
    const btn = (label: string) => Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === label);
    await click(btn(t("mem.as_retry")));
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/auto-sync?action=retry", "POST", { action: "retry" });

    await click(btn(t("mem.as_abandon")));
    expect(apiJSON).not.toHaveBeenCalledWith("api/agents/memory/auto-sync?action=abandon", "POST", { action: "abandon" });
    await click(confirmBtn()); // the member agrees
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/auto-sync?action=abandon", "POST", { action: "abandon" });
  });
});

describe("problems the Agent reports", () => {
  it("says an unreadable state, offers only the way out, and lists the other problems", async () => {
    api.mockResolvedValue({
      phase: "unreadable",
      want: true,
      settled: false,
      failing: true,
      nativeMemoryOff: true,
      problems: ["state_unreadable", "codex_flag"],
    });
    setSettings({ ...settingsDefaults(), agentMemory: true });
    await mount();
    const text = host!.textContent ?? "";
    expect(text).toContain(t("mem.as_phase_unreadable"));
    expect(text).toContain(t("mem.as_problem_state_unreadable"));
    expect(text).toContain(t("mem.as_problem_codex_flag"));
    const labels = Array.from(host!.querySelectorAll("button")).map((b) => b.textContent);
    expect(labels).toContain(t("mem.as_abandon"));
    expect(labels).not.toContain(t("mem.as_retry"));
  });
});
