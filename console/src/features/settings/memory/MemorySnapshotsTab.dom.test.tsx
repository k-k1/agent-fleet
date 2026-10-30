// The Memory snapshots entry (ADR 0022): a status header over History / Targets / Export & import.
// Pinned here: where each control lives after the split (the codex memories toggle and Automatic
// snapshots in Targets, Restore this point in History, the bundle export in Export & import),
// view switching, the remembered view, deep links, and that none of Agent Fleet's own memory
// controls came along.
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
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
const ws = { state: "running" };
const startWs = vi.fn();
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) =>
    sel({ state: ws.state, start: startWs }),
  wsStartBusy: () => false,
}));

const { MemorySnapshotsTab } = await import("./MemorySnapshotsTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { t } = await import("../../../lib/i18n/index.ts");
const { setSetting } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const flush = () =>
  act(async () => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  });
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<MemorySnapshotsTab />));
  await flush();
}
function unmount() {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
}
const tab = (key: string) => host!.querySelector<HTMLButtonElement>(`[role=tab][id$="-tab-${key}"]`)!;
const goto = async (key: string) => {
  await act(async () => tab(key).click());
  await flush();
};
const text = () => host!.querySelector("[role=tabpanel]")?.textContent ?? "";
const selectedTab = () => host!.querySelector("[role=tab][aria-selected=true]")?.id.replace(/^.*-tab-/, "");

beforeEach(() => {
  ws.state = "running";
  localStorage.clear();
  api.mockImplementation((path: string) => {
    if (path.startsWith("api/agents/memory/roots"))
      return Promise.resolve({
        roots: [{ kind: "claude", label: "Claude", scopes: true, files: 3, bytes: 100, projects: [] }],
        inactive: [{ kind: "codex", label: "Codex", reason: "codex_memories_disabled", toggleable: true, enabled: false }],
        auto: true,
        autoLocked: false,
        lastSnapshot: "2026-10-04T10:00:00Z",
      });
    if (path.startsWith("api/agents/memory/snapshots"))
      return Promise.resolve({
        snapshots: [
          { rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 },
        ],
      });
    return Promise.resolve({ diff: "" });
  });
});

afterEach(() => {
  unmount();
  act(() => useSettingsUI.getState().closeSettings());
  api.mockReset();
  apiJSON.mockReset();
  startWs.mockClear();
});

describe("MemorySnapshotsTab diff pane without a selection", () => {
  const loadingText = t("common.loading");
  const diffPane = () => host!.querySelector(".mem-diff");
  const setValue = async (el: HTMLInputElement, v: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const rootsOk = {
    roots: [{ kind: "claude", label: "Claude", scopes: true, files: 3, bytes: 100, projects: [] }],
    inactive: [],
    auto: true,
    autoLocked: false,
    lastSnapshot: "",
  };
  const stub = (snapshots: Promise<unknown> | unknown, diff?: Promise<unknown>) =>
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/roots")) return Promise.resolve(rootsOk);
      if (path.startsWith("api/agents/memory/snapshots")) return Promise.resolve(snapshots);
      return diff ?? Promise.resolve({ diff: "" });
    });

  it("shows no loading text next to 'No snapshots yet' when the history is empty", async () => {
    stub({ snapshots: [] });
    await mount();
    expect(text()).toContain(t("mem.history_empty"));
    expect(text()).not.toContain(loadingText);
    expect(diffPane()?.textContent ?? "").toBe("");
  });

  it("shows the load error, not 'No snapshots yet', when the history list failed to load", async () => {
    stub({ error: "boom" });
    await mount();
    expect(text()).toContain(t("mem.history_failed", { msg: "boom" }));
    expect(text()).not.toContain(t("mem.history_empty"));
    expect(text()).not.toContain(loadingText);
    expect(host!.querySelector(".mem-diff-head")).toBeNull();
    expect(diffPane()?.textContent ?? "").toBe("");
  });

  it("re-requests the history on Retry and lists what comes back", async () => {
    stub({ error: "boom" });
    await mount();
    const calls = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/snapshots")).length;
    const before = calls();
    stub({ snapshots: [{ rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 }] });
    const retry = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("mem.history_retry"),
    )!;
    await act(async () => retry.click());
    await flush();
    expect(calls()).toBe(before + 1);
    expect(text()).not.toContain(t("mem.history_failed", { msg: "boom" }));
    expect(host!.querySelector(".mem-snap")).not.toBeNull();
  });

  it("disables the date jump while the list error is shown, so no row and diff appear beside it", async () => {
    stub({ error: "boom" });
    await mount();
    await setValue(host!.querySelector<HTMLInputElement>('input[type="datetime-local"]')!, "2026-10-03T13:00");
    const go = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("mem.jump_go"),
    )!;
    expect(go.disabled).toBe(true);
    await act(async () => go.click());
    expect(api.mock.calls.some((c) => String(c[0]).includes("before="))).toBe(false);
    expect(host!.querySelector(".mem-diff-head")).toBeNull();
  });

  it("drops a date jump that returns after the list reload failed", async () => {
    const snap = { rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 };
    let listFails = false;
    let resolveJump!: (v: unknown) => void;
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/roots")) return Promise.resolve(rootsOk);
      if (path.includes("before=")) return new Promise((r) => (resolveJump = r));
      if (path.startsWith("api/agents/memory/snapshots"))
        return Promise.resolve(listFails ? { error: "boom" } : { snapshots: [] });
      return Promise.resolve({ diff: "" });
    });
    apiJSON.mockResolvedValue({ committed: true });
    await mount();
    await setValue(host!.querySelector<HTMLInputElement>('input[type="datetime-local"]')!, "2026-10-03T13:00");
    const btn = (label: string) =>
      Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === label)!;
    await act(async () => btn(t("mem.jump_go")).click()); // in flight
    listFails = true;
    await act(async () => btn(t("mem.snapshot_now")).click()); // reload fails
    await flush();
    expect(text()).toContain(t("mem.history_failed", { msg: "boom" }));
    await act(async () => resolveJump({ snapshots: [snap] }));
    await flush();
    expect(text()).toContain(t("mem.history_failed", { msg: "boom" }));
    expect(host!.querySelector(".mem-diff-head")).toBeNull();
    expect(host!.querySelector(".mem-snap")).toBeNull();
  });

  describe("when the roots request fails", () => {
    const one = { rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 };
    const count = (prefix: string) => api.mock.calls.filter((c) => String(c[0]).startsWith(prefix)).length;
    const stubRoots = (roots: unknown) =>
      api.mockImplementation((path: string) => {
        if (path.startsWith("api/agents/memory/roots")) return Promise.resolve(roots);
        if (path.startsWith("api/agents/memory/snapshots")) return Promise.resolve({ snapshots: [one] });
        return Promise.resolve({ diff: "" });
      });
    const retryBtn = () =>
      Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === t("mem.history_retry"))!;

    it("does not leave the history on 'Loading…'", async () => {
      stubRoots({ error: "boom" });
      await mount();
      expect(text()).toContain(t("mem.history_failed", { msg: "boom" }));
      expect(text()).not.toContain(loadingText);
      expect(host!.querySelector(".mem-diff-head")).toBeNull();
      expect(host!.querySelector(".mem-status")!.textContent).not.toContain(loadingText);
    });

    it("recovers on Retry: roots and the list are re-read and the list shows", async () => {
      stubRoots({ error: "boom" });
      await mount();
      stubRoots(rootsOk);
      await act(async () => retryBtn().click());
      await flush();
      expect(text()).not.toContain(t("mem.history_failed", { msg: "boom" }));
      expect(host!.querySelector(".mem-snap")).not.toBeNull();
      expect(host!.querySelector(".mem-warn")).toBeNull();
    });

    it("requests once per load, with no loop, and disables the date jump", async () => {
      stubRoots({ error: "boom" });
      await mount();
      await flush();
      expect(count("api/agents/memory/roots")).toBe(1);
      expect(count("api/agents/memory/snapshots")).toBe(1);
      const input = host!.querySelector<HTMLInputElement>("input[type=datetime-local]")!;
      await setValue(input, "2026-10-04T12:00");
      const go = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === t("mem.jump_go"))!;
      expect(go.disabled).toBe(true);
    });

    it("shows a Retry, not 'Loading…', under Targets", async () => {
      stubRoots({ error: "boom" });
      await mount();
      await goto("targets");
      expect(text()).not.toContain(loadingText);
      stubRoots(rootsOk);
      await act(async () => retryBtn().click());
      await flush();
      expect(text()).toContain("Claude");
    });
  });

  it("keeps 'No snapshots yet' for a successful empty list, without a retry", async () => {
    stub({ snapshots: [] });
    await mount();
    expect(text()).toContain(t("mem.history_empty"));
    expect(text()).not.toContain(t("mem.history_retry"));
  });

  it("still shows the loading text while a selected snapshot's diff is being fetched", async () => {
    let resolve!: (v: unknown) => void;
    const pending = new Promise((r) => (resolve = r));
    stub(
      { snapshots: [{ rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 }] },
      pending,
    );
    await mount();
    expect(diffPane()!.textContent).toContain(loadingText);
    await act(async () => resolve({ diff: "" }));
    await flush();
    expect(diffPane()!.textContent).not.toContain(loadingText);
  });

  it("shows a diff, not the loading text, when only hidden Agent Fleet rows exist", async () => {
    stub({ snapshots: [{ rev: "f1", short: "f1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "agent-memory", kinds: ["claude"], projects: [], files: 1 }] });
    await mount();
    expect(host!.querySelector(".mem-diff-head code")!.textContent).toBe("f1");
    expect(diffPane()!.textContent).not.toContain(loadingText);
  });

  it("shows no loading text when a remount finds the history emptied", async () => {
    stub({ snapshots: [{ rev: "r1", short: "r1", at: "2026-10-04T10:00:00Z", subject: "s", trigger: "auto", kinds: ["claude"], projects: [], files: 3 }] });
    await mount();
    expect(host!.querySelector(".mem-diff-head code")).not.toBeNull();
    stub({ snapshots: [] });
    unmount();
    await mount();
    expect(host!.querySelector(".mem-diff-head")).toBeNull();
    expect(text()).not.toContain(loadingText);
  });
});

describe("MemorySnapshotsTab", () => {
  it("opens on History with Restore this point, and no other view's controls", async () => {
    await mount();
    expect(Array.from(host!.querySelectorAll("[role=tab]")).map((b) => b.textContent)).toEqual([
      t("mem.view_history"),
      t("mem.view_targets"),
      t("mem.view_transfer"),
    ]);
    expect(selectedTab()).toBe("history");
    expect(text()).toContain(t("mem.restore"));
    expect(text()).not.toContain(t("mem.auto_label"));
    expect(text()).not.toContain(t("mem.export_do"));
  });

  it("keeps the status header on every view: last snapshot, automatic on, Snapshot now", async () => {
    await mount();
    for (const v of ["history", "targets", "transfer"]) {
      await goto(v);
      const head = host!.querySelector(".mem-status")!.textContent!;
      expect(head, v).toContain(t("mem.status_auto_on"));
      expect(head, v).toContain(t("mem.snapshot_now"));
      expect(head, v).not.toContain(t("mem.never"));
    }
  });

  // An unread or failed roots load is not "automatic off" and not "no snapshots yet".
  it("asserts nothing in the header until the roots are read", async () => {
    let release: (v: unknown) => void = () => {};
    const rootsReply = new Promise((r) => (release = r));
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) => (path.startsWith("api/agents/memory/roots") ? rootsReply : base(path)));
    await mount();
    const head = () => host!.querySelector(".mem-status")!.textContent!;
    expect(head()).not.toContain(t("mem.status_auto_off"));
    expect(head()).not.toContain(t("mem.status_auto_on"));
    expect(head()).not.toContain(t("mem.never"));
    await act(async () => release({ roots: [], auto: true, autoLocked: false, lastSnapshot: "2026-10-04T10:00:00Z" }));
    await flush();
    expect(head()).toContain(t("mem.status_auto_on"));
  });

  it("asserts nothing when the roots fail to load", async () => {
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) =>
      path.startsWith("api/agents/memory/roots") ? Promise.resolve({ error: { code: "boom", message: "boom" } }) : base(path),
    );
    await mount();
    const head = host!.querySelector(".mem-status")!.textContent!;
    expect(head).not.toContain(t("mem.status_auto_off"));
    expect(head).not.toContain(t("mem.never"));
    expect(host!.querySelector(".mem-warn")).toBeTruthy();
  });

  it("puts the roots, the codex memories toggle and Automatic snapshots under Targets", async () => {
    await mount();
    await goto("targets");
    expect(text()).toContain("Claude");
    expect(text()).toContain("Codex");
    // The codex toggle is the on/off control on its root's row.
    expect(host!.querySelector(".mem-root-off .seg-btn")).toBeTruthy();
    expect(text()).toContain(t("mem.codex_cost_hint"));
    expect(text()).toContain(t("mem.auto_label"));
    expect(text()).not.toContain(t("mem.restore"));
  });

  describe("the codex dual-memory hint under Targets", () => {
    const rootsWith = (codex: Record<string, unknown>) =>
      api.mockImplementation((path: string) => {
        if (path.startsWith("api/agents/memory/roots"))
          return Promise.resolve({
            roots: [{ kind: "claude", label: "Claude", scopes: true, files: 3, bytes: 100, projects: [] }],
            inactive: [{ kind: "codex", label: "Codex", reason: "codex_memories_pending", toggleable: true, ...codex }],
            auto: true,
            autoLocked: false,
          });
        return Promise.resolve(path.startsWith("api/agents/memory/snapshots") ? { snapshots: [] } : { diff: "" });
      });
    afterEach(() => setSetting("agentMemory", false));

    it("shows when Agent memory is on and codex memories are enabled", async () => {
      setSetting("agentMemory", true);
      rootsWith({ enabled: true });
      await mount();
      await goto("targets");
      expect(text()).toContain(t("mem.codex_dual_hint"));
    });

    it("stays away when codex memories are off or Agent memory is off", async () => {
      setSetting("agentMemory", true);
      rootsWith({ enabled: false });
      await mount();
      await goto("targets");
      expect(text()).not.toContain(t("mem.codex_dual_hint"));
      unmount();
      setSetting("agentMemory", false);
      rootsWith({ enabled: true });
      await mount();
      await goto("targets");
      expect(text()).not.toContain(t("mem.codex_dual_hint"));
    });
  });

  it("puts the bundle export under Export & import", async () => {
    await mount();
    await goto("transfer");
    expect(text()).toContain(t("mem.export_do"));
    expect(text()).not.toContain(t("mem.auto_label"));
  });

  it("carries none of Agent Fleet's own memory controls", async () => {
    await mount();
    for (const v of ["history", "targets", "transfer"]) {
      await goto(v);
      expect(host!.textContent, v).not.toContain(t("mem.af_switch"));
      expect(host!.textContent, v).not.toContain(t("mem.br_title"));
    }
  });

  it("remembers its view apart from the Agent memory entry's", async () => {
    localStorage.setItem("af-settings-views", JSON.stringify({ memory: "changes" }));
    await mount();
    expect(selectedTab()).toBe("history");
    await goto("targets");
    unmount();
    await mount();
    expect(selectedTab()).toBe("targets");
    expect(JSON.parse(localStorage.getItem("af-settings-views")!)).toEqual({
      memory: "changes",
      memorysnapshots: "targets",
    });
  });

  it("opens the view a deep link names", async () => {
    act(() => useSettingsUI.getState().openSettings("memorysnapshots:transfer"));
    await mount();
    expect(selectedTab()).toBe("transfer");
  });

  it("offers Start and calls nothing while the workspace is stopped", async () => {
    ws.state = "stopped";
    await mount();
    expect(host!.textContent).toContain(t("mem.ws_required_title"));
    expect(host!.querySelector("[role=tab]")).toBeNull();
    expect(host!.textContent).not.toContain(t("mem.af_switch"));
    const start = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("mem.start_ws"),
    )!;
    await act(async () => start.click());
    expect(startWs).toHaveBeenCalledTimes(1);
    expect(api).not.toHaveBeenCalled();
  });
});

// History hides the rows Agent Fleet's own memory writes leave (trigger "agent-memory"): Agent
// memory > Changes already lists them. The hidden ones must stay reachable by date jump and restore.
describe("MemorySnapshotsTab history filter for Agent Fleet memory writes", () => {
  const snap = (rev: string, at: string, trigger: string) => ({
    rev,
    short: rev,
    at,
    subject: "s",
    trigger,
    kinds: ["claude"],
    projects: [],
    files: 1,
  });
  const newest = snap("a1", "2026-10-04T10:00:00Z", "auto");
  const af1 = snap("f1", "2026-10-04T09:00:00Z", "agent-memory");
  const af2 = snap("f2", "2026-10-04T08:00:00Z", "agent-memory");
  const older = snap("m1", "2026-10-04T07:00:00Z", "manual");
  const hiddenAf = snap("f0", "2026-10-03T12:00:00Z", "agent-memory");

  beforeEach(() => {
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/snapshots?limit=1&before="))
        return Promise.resolve({ snapshots: [hiddenAf] });
      if (path.startsWith("api/agents/memory/snapshots"))
        return Promise.resolve({ snapshots: [newest, af1, af2, older] });
      return base(path);
    });
  });

  const rows = () => Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-snap"));
  const toggle = () => host!.querySelector<HTMLInputElement>('.mem-jump input[type="checkbox"]')!;
  const hint = () => host!.querySelector(".mem-snap-hidden");
  const restoreBtn = () =>
    Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-diff-head button")).find(
      (b) => b.textContent === t("mem.restore"),
    )!;
  const setValue = async (el: HTMLInputElement, v: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const jump = async () => {
    await setValue(host!.querySelector<HTMLInputElement>('input[type="datetime-local"]')!, "2026-10-03T13:00");
    await act(async () => {
      Array.from(host!.querySelectorAll<HTMLButtonElement>("button"))
        .find((b) => b.textContent === t("mem.jump_go"))!
        .click();
    });
    await flush();
  };

  it("hides the Agent Fleet memory writes by default and leaves a count with a link to Changes", async () => {
    await mount();
    expect(rows().map((r) => r.textContent)).toEqual([
      expect.stringContaining(t("mem.trigger_auto")),
      expect.stringContaining(t("mem.trigger_manual")),
    ]);
    expect(toggle().checked).toBe(false);
    // The two adjacent writes fold into one hint.
    expect(host!.querySelectorAll(".mem-snap-hidden")).toHaveLength(1);
    expect(hint()!.textContent).toContain(t("mem.hist_af_hidden", { n: 2 }));
    const link = hint()!.querySelector<HTMLButtonElement>("button")!;
    expect(link.textContent).toBe(t("mem.hist_af_changes"));
    await act(async () => link.click());
    expect(useSettingsUI.getState().settingsSection).toBe("memory");
    expect(useSettingsUI.getState().settingsView).toBe("changes");
  });

  it("lists them with the toggle on, and remembers the toggle", async () => {
    await mount();
    await act(async () => toggle().click());
    expect(rows()).toHaveLength(4);
    expect(hint()).toBeNull();
    expect(localStorage.getItem("af-snapshots-show-af")).toBe("1");
    unmount();
    await mount();
    expect(toggle().checked).toBe(true);
    expect(rows()).toHaveLength(4);
  });

  it("reveals a hidden snapshot a date jump lands on, and its Restore this point still opens", async () => {
    await mount();
    expect(rows()).toHaveLength(2);
    await jump();
    const active = host!.querySelector(".mem-snap.active")!;
    expect(active.textContent).toContain(t("mem.trigger_agent_memory"));
    expect(host!.querySelector(".mem-diff-head code")!.textContent).toBe("f0");
    expect(api).toHaveBeenCalledWith("api/agents/memory/diff?to=f0");
    await act(async () => restoreBtn().click());
    expect(host!.textContent).toContain(t("mem.restore_title"));
    // The toggle itself is untouched: the jump revealed one row, not the filter.
    expect(toggle().checked).toBe(false);
  });

  it("keeps a hidden row listed while it is the selected one, and hides it again once another is picked", async () => {
    await mount();
    await act(async () => toggle().click());
    await act(async () => rows()[1].click()); // f1, an Agent Fleet memory write
    expect(host!.querySelector(".mem-diff-head code")!.textContent).toBe("f1");
    await act(async () => toggle().click());
    expect(rows().map((r) => r.querySelector(".mem-snap-when")!.nextSibling!.textContent)).toEqual([
      t("mem.trigger_auto"),
      t("mem.trigger_agent_memory"),
      t("mem.trigger_manual"),
    ]);
    expect(host!.querySelector(".mem-diff-head code")!.textContent).toBe("f1");
    expect(restoreBtn()).toBeTruthy();
    await act(async () => rows()[0].click());
    expect(rows()).toHaveLength(2);
    expect(hint()!.textContent).toContain(t("mem.hist_af_hidden", { n: 2 }));
  });
});
