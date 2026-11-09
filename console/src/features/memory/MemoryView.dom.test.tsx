// The Memory pane (#3720): the viewer, the change list and the proposals mounted from the
// components Settings used. Pinned: the pane's own `memoryView` picks the panel, a tab click
// retargets the pane (the choice survives a tab switch), a stopped workspace offers Start, and
// Settings keeps the switches with a link that opens this pane.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(async () => ({})),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
const ws = { state: "running" };
const startWs = vi.fn();
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: Object.assign(
    (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: ws.state, start: startWs }),
    { getState: () => ({ state: ws.state, start: startWs }), subscribe: () => () => {} },
  ),
  wsStartBusy: () => false,
}));

const { MemoryView } = await import("./MemoryView.tsx");
const { MemoryTab } = await import("../settings/memory/MemoryTab.tsx");
const { useLayoutStore } = await import("../../layout/store.ts");
const { allViews, freshLayout } = await import("../../layout/ops.ts");
const { useSettingsUI } = await import("../settings/store.ts");
const { setLocale, t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = () =>
  act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  await flush();
}
const headings = () => Array.from(host!.querySelectorAll("[role=tabpanel] h4")).map((h) => h.textContent);
const tabs = () => Array.from(host!.querySelectorAll("[role=tab]")).map((b) => b.textContent);
const memoryPane = () => allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "memory");

beforeEach(() => {
  setLocale("en");
  ws.state = "running";
  api.mockResolvedValue({});
  useLayoutStore.setState({ layout: freshLayout() });
  useSettingsUI.getState().closeSettings();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  startWs.mockClear();
});

describe("MemoryView", () => {
  it("offers the viewer, the change list and the proposals", async () => {
    await mount(<MemoryView paneId="x" view="memories" />);
    expect(tabs()).toEqual([t("mem.view_memories"), t("mem.view_changes"), t("mem.view_proposals")]);
    expect(headings()).toEqual([t("mem.br_title")]);
  });

  it.each([
    ["changes", "mem.af_changes_title"],
    ["proposals", "mem.cs_title"],
  ] as const)("shows the %s panel the pane asks for", async (view, heading) => {
    await mount(<MemoryView paneId="x" view={view} />);
    expect(headings()).toEqual([t(heading)]);
  });

  it("retargets the pane when a tab is clicked", async () => {
    useLayoutStore.getState().openTarget({ content: { kind: "memory", memoryView: "memories" } });
    const pane = memoryPane()!;
    await mount(<MemoryView paneId={pane.id} view="memories" />);
    await act(async () => host!.querySelector<HTMLButtonElement>("[role=tab][id$='-tab-proposals']")!.click());
    expect(memoryPane()!.content).toEqual({ kind: "memory", memoryView: "proposals" });
  });

  it("offers Start while the workspace is stopped", async () => {
    ws.state = "stopped";
    await mount(<MemoryView paneId="x" view="proposals" />);
    expect(host!.textContent).toContain(t("mem.ws_required_title"));
    await act(async () => Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === t("mem.start_ws"))!.click());
    expect(startWs).toHaveBeenCalled();
  });
});

describe("Settings > Agent memory", () => {
  it("keeps the switches and import/export, and the lists moved out", async () => {
    await mount(<MemoryTab />);
    expect(tabs()).toEqual([t("mem.view_settings"), t("mem.view_importexport")]);
  });

  it("opens the Memory view from the link, closing the modal", async () => {
    act(() => useSettingsUI.getState().openSettings("memory"));
    await mount(<MemoryTab />);
    await act(async () => Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === t("mem.open_view"))!.click());
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(memoryPane()!.content).toEqual({ kind: "memory", memoryView: "memories" });
  });
});
