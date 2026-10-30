// The Agent memory entry (Settings > Agent memory): a status header, a view-nav and one view at a
// time. Pinned here: which panel sits in which view (a panel lost in the move shows up as a
// missing heading), view switching, the view remembered per entry, the deep-link targets that
// other screens' pointers rely on, and the stopped workspace, where only Settings works (#1735).
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
// The flip asks first (#1972); here the member says yes. What the dialog says is pinned in
// memoryAutoSync.dom.test.tsx.
const askConfirm = vi.fn(async (_opts: unknown) => true);
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => askConfirm }));
const ws = { state: "running" };
const startWs = vi.fn();
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) =>
    sel({ state: ws.state, start: startWs }),
  wsStartBusy: () => false,
}));

const { MemoryTab } = await import("./MemoryTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { setSettings, settingsDefaults, getSettings, normalizeExternalMemory } = await import(
  "../../../lib/settings.ts"
);
const { t } = await import("../../../lib/i18n/index.ts");

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
  await act(async () => root!.render(<MemoryTab />));
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
// Panel headings only: prose such as the Browse intro mentions "Changes" without being that panel.
const headings = () => Array.from(host!.querySelectorAll("[role=tabpanel] h4")).map((h) => h.textContent);
const selectedTab = () => host!.querySelector("[role=tab][aria-selected=true]")?.id.replace(/^.*-tab-/, "");
const switchRow = (label: string) =>
  Array.from(host!.querySelectorAll(".ds-row")).find((r) => r.querySelector(".ds-label")?.textContent === label);

beforeEach(() => {
  ws.state = "running";
  localStorage.clear();
  setSettings(settingsDefaults());
  // Every panel reads an empty store: enough to render headings, nothing more.
  api.mockResolvedValue({});
});

afterEach(() => {
  unmount();
  act(() => useSettingsUI.getState().closeSettings());
  api.mockReset();
  apiJSON.mockReset();
  askConfirm.mockClear();
  startWs.mockClear();
  setSettings(settingsDefaults());
});

describe("MemoryTab views", () => {
  it("offers the four views and opens on Memories, with the two lists and nothing else", async () => {
    await mount();
    expect(Array.from(host!.querySelectorAll("[role=tab]")).map((b) => b.textContent)).toEqual([
      t("mem.view_memories"),
      t("mem.view_changes"),
      t("mem.view_importexport"),
      t("mem.view_settings"),
    ]);
    expect(selectedTab()).toBe("memories");
    expect(headings()).toEqual([t("mem.br_title")]);
    expect(switchRow(t("mem.af_switch"))).toBeUndefined();
  });

  it("shows each view's panels after switching, and drops the previous view's", async () => {
    await mount();
    await goto("changes");
    expect(headings()).toEqual([t("mem.af_changes_title")]);

    await goto("importexport");
    // The hub: one card per source; the wizards open from their Start buttons.
    expect(headings()).toEqual([
      t("mem.ci_title"),
      t("mem.mi_title"),
      t("mem.bi_title"),
      t("mem.ce_title"),
      t("mem.pm_title"),
    ]);

    await goto("settings");
    expect(switchRow(t("mem.af_switch"))).toBeTruthy();
    expect(switchRow(t("mem.ext_switch"))).toBeTruthy();
    expect(headings()).toEqual([]);
  });

  // #2329: the "How to connect" link belongs beside the outside-client setting, and only there.
  it("mounts the outside-access help link in Settings, next to the setting", async () => {
    await mount();
    const link = () => Array.from(host!.querySelectorAll("button")).filter((b) => b.textContent === t("mem.ext_help"));
    expect(link()).toHaveLength(0);
    await goto("settings");
    expect(link()).toHaveLength(1);
    const row = switchRow(t("mem.ext_switch"))!;
    expect(row.parentElement!.contains(link()[0])).toBe(true);
  });

  it("does not carry the snapshot panels any more", async () => {
    await mount();
    for (const v of ["memories", "changes", "importexport", "settings"]) {
      await goto(v);
      expect(text(), v).not.toContain(t("mem.snapshot_now"));
      expect(text(), v).not.toContain(t("mem.auto_label"));
      // The hub's "Export" group heading is not the snapshot export button.
      const buttons = Array.from(host!.querySelectorAll("[role=tabpanel] button")).map((b) => b.textContent);
      expect(buttons, v).not.toContain(t("mem.export_do"));
    }
  });

  it("remembers the last view for the next time the entry is opened", async () => {
    await mount();
    await goto("changes");
    unmount();
    await mount();
    expect(selectedTab()).toBe("changes");
    expect(headings()).toEqual([t("mem.af_changes_title")]);
  });

  it("opens the view a deep link names, and says it was consumed", async () => {
    act(() => useSettingsUI.getState().openSettings("memory:importexport"));
    await mount();
    expect(selectedTab()).toBe("importexport");
    expect(useSettingsUI.getState().settingsView).toBeNull();
    expect(useSettingsUI.getState().settingsSection).toBe("memory");
  });

  it("follows a deep link made while the entry is already on screen", async () => {
    await mount();
    expect(selectedTab()).toBe("memories");
    act(() => useSettingsUI.getState().openSettings("memory:changes"));
    await flush();
    expect(selectedTab()).toBe("changes");
    expect(useSettingsUI.getState().settingsView).toBeNull();
  });

  it("ignores a view name this entry does not have, instead of keeping it for later", async () => {
    act(() => useSettingsUI.getState().openSettings("memory:nonsense"));
    await mount();
    expect(selectedTab()).toBe("memories");
    expect(useSettingsUI.getState().settingsView).toBeNull();
  });

  // err.memory_disabled and agents.note_agent_memory_moved name this place in words. The path
  // they name has to be a real one: entry "Agent memory", view "Settings", where the switch is.
  it("keeps the pointers that name Agent memory > Settings true", async () => {
    for (const msg of [
      t("err.memory_disabled"),
      t("agents.note_agent_memory_moved"),
    ]) {
      expect(msg).toContain(`${t("set.tab_memory")} > ${t("mem.view_settings")}`);
    }
    act(() => useSettingsUI.getState().openSettings("memory:settings"));
    await mount();
    expect(selectedTab()).toBe("settings");
    expect(switchRow(t("mem.af_switch"))).toBeTruthy();
  });
});

describe("MemoryTab status header", () => {
  it("summarises from what the panels load", async () => {
    setSettings({ ...settingsDefaults(), agentMemory: true });
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/entries/list"))
        return Promise.resolve({
          entries: [
            { name: "a", scope: "user", pinned: false, uses: 0 },
            { name: "b", scope: "project", project: { id: "p1", display: "app" }, pinned: false, uses: 0 },
            { name: "c", scope: "project", project: { id: "p1", display: "app" }, pinned: false, uses: 0 },
          ],
        });
      if (path.startsWith("api/agents/memory/entries/changes"))
        return Promise.resolve({ changes: [{ commit: "c1", at: "2026-10-04T10:00:00Z" }] });
      if (path.startsWith("api/agents/memory/auto-sync"))
        return Promise.resolve({ phase: "on", want: true, settled: true, nativeMemoryOff: true, problems: ["state_not_saved"] });
      return Promise.resolve({});
    });
    await mount();
    const head = host!.querySelector(".mem-status")!.textContent!;
    expect(head).toContain(t("mem.status_on"));
    expect(head).toContain(t("mem.status_count", { n: 3 }));
    expect(head).toContain(t("mem.status_projects", { n: 1 }));
    expect(head).toContain(t("mem.status_sync_problem"));
    // The sync warning leads to where the sync is shown.
    const review = Array.from(host!.querySelectorAll<HTMLButtonElement>(".mem-status button")).find(
      (b) => b.textContent === t("mem.status_sync_review"),
    )!;
    await act(async () => review.click());
    expect(selectedTab()).toBe("settings");
  });

  it("says off, and no problem, for a fresh workspace", async () => {
    await mount();
    const head = host!.querySelector(".mem-status")!.textContent!;
    expect(head).toContain(t("mem.status_off"));
    expect(head).not.toContain(t("mem.status_sync_problem"));
  });
});

// The header follows the sync run and the switch, not just the first read.
describe("MemoryTab status header over time", () => {
  const sync = (o: object) => ({ phase: "on", want: true, settled: true, nativeMemoryOff: true, ...o });
  const entry = (n: string) => ({ name: n, scope: "user", pinned: false, uses: 0 });
  const head = () => host!.querySelector(".mem-status")!.textContent!;
  const advance = async (ms: number) => {
    await act(async () => {
      vi.advanceTimersByTime(ms);
    });
    await flush();
  };

  beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }));
  afterEach(() => vi.useRealTimers());

  it("shows the count after an import that was still running when the switch was flipped", async () => {
    let done = false;
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/entries/list"))
        return Promise.resolve({ entries: done ? [entry("a"), entry("b"), entry("c")] : [] });
      if (path.startsWith("api/agents/memory/auto-sync"))
        return Promise.resolve(done ? sync({}) : sync({ phase: "importing", settled: false }));
      return Promise.resolve({});
    });
    setSettings({ ...settingsDefaults(), agentMemory: true });
    await mount();
    await goto("settings");
    expect(head()).toContain(t("mem.status_count", { n: 0 }));
    done = true;
    await advance(4500);
    expect(head()).toContain(t("mem.status_count", { n: 3 }));
  });

  it("clears the sync warning once Retry succeeds, and raises one for a run that fails later", async () => {
    let st: object = sync({ failing: true });
    api.mockImplementation((path: string) =>
      path.startsWith("api/agents/memory/auto-sync") ? Promise.resolve(st) : Promise.resolve({}),
    );
    setSettings({ ...settingsDefaults(), agentMemory: true });
    await mount();
    expect(head()).toContain(t("mem.status_sync_problem"));
    st = sync({});
    await advance(4500);
    expect(head()).not.toContain(t("mem.status_sync_problem"));

    // A run in progress that fails afterwards.
    st = sync({ phase: "importing", settled: false });
    await goto("changes");
    expect(head()).not.toContain(t("mem.status_sync_problem"));
    st = sync({ phase: "importing", settled: false, failing: true });
    await advance(4500);
    expect(head()).toContain(t("mem.status_sync_problem"));
  });

  it("re-reads when the switch is flipped, and when the member moves between views", async () => {
    let n = 0;
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/agents/memory/entries/list")) return Promise.resolve({ entries: Array.from({ length: n }, (_, i) => entry("m" + i)) });
      return Promise.resolve(path.startsWith("api/agents/memory/auto-sync") ? sync({}) : {});
    });
    await mount();
    expect(head()).toContain(t("mem.status_off"));
    n = 2;
    await goto("changes");
    expect(head()).toContain(t("mem.status_count", { n: 2 }));
    setSettings({ ...settingsDefaults(), agentMemory: true });
    n = 5;
    await flush();
    expect(head()).toContain(t("mem.status_on"));
    expect(head()).toContain(t("mem.status_count", { n: 5 }));
  });
});


// The history and the import need the Agent, so every view but Settings shows the start prompt;
// the switches are ui-prefs the member sets for the next start, and this page is their only home.
describe("MemoryTab while the workspace is stopped", () => {
  beforeEach(() => {
    ws.state = "stopped";
  });

  it("opens on Settings, even when another view was the last one used", async () => {
    localStorage.setItem("af-settings-views", JSON.stringify({ memory: "changes" }));
    await mount();
    expect(selectedTab()).toBe("settings");
    expect(switchRow(t("mem.af_switch"))).toBeTruthy();
  });

  it("still offers the Agent Fleet memory switch, both ways, without calling the Agent", async () => {
    await mount();
    const row = switchRow(t("mem.af_switch"))!;
    expect(getSettings().agentMemory).toBe(false);
    const [on, off] = Array.from(row.querySelectorAll<HTMLButtonElement>(".seg-btn"));
    await act(async () => on.click());
    expect(getSettings().agentMemory).toBe(true);
    await act(async () => off.click());
    expect(getSettings().agentMemory).toBe(false);
    expect(askConfirm).toHaveBeenCalledTimes(2);
    // No Agent to ask: no preview, no wake. It reconciles when the workspace starts.
    expect(api).not.toHaveBeenCalled();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  // #1988: the outside-client setting is off by default, has three states, and needs no Agent and
  // no confirmation.
  it("offers the outside-client setting, off by default, without calling the Agent", async () => {
    await mount();
    const row = switchRow(t("mem.ext_switch"))!;
    expect(getSettings().externalMemory).toBe("off");
    const [, read, write] = Array.from(row.querySelectorAll<HTMLButtonElement>(".seg-btn"));
    await act(async () => read.click());
    expect(getSettings().externalMemory).toBe("read");
    await act(async () => write.click());
    expect(getSettings().externalMemory).toBe("write");
    expect(getSettings().agentMemory).toBe(false);
    expect(askConfirm).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
  });

  it("offers Start instead of the other views, and calls nothing", async () => {
    await mount();
    for (const v of ["memories", "changes", "importexport"]) {
      await goto(v);
      expect(text(), v).toContain(t("mem.ws_required_title"));
      expect(switchRow(t("mem.af_switch")), v).toBeUndefined();
      expect(text(), v).not.toContain(t("mem.br_title"));
    }
    const start = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("mem.start_ws"),
    )!;
    await act(async () => start.click());
    expect(startWs).toHaveBeenCalledTimes(1);
    expect(api).not.toHaveBeenCalled();
  });

  it("does not forget the remembered view because it opened on Settings", async () => {
    localStorage.setItem("af-settings-views", JSON.stringify({ memory: "changes" }));
    await mount();
    unmount();
    ws.state = "running";
    await mount();
    expect(selectedTab()).toBe("changes");
  });
});

// The Agent reads only "read" and "write"; anything else shown as on would show one thing and
// enforce another.
describe("normalizeExternalMemory", () => {
  it("keeps the two on-states and turns everything else off", () => {
    expect(normalizeExternalMemory("read")).toBe("read");
    expect(normalizeExternalMemory("write")).toBe("write");
    for (const v of [undefined, null, "", "on", "WRITE", true, 1, {}]) expect(normalizeExternalMemory(v)).toBe("off");
  });
});
