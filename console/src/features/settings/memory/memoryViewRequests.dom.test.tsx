// "section:view" requests through the real Settings dialog: a request addressed to the other
// memory entry must reach it (the entry being left must not consume it), and on a phone a request
// naming a view opens the section's detail, whether the dialog was closed or already on that section.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: Object.assign(
    (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: "running", start: () => {} }),
    { getState: () => ({ state: "running", start: () => {} }), subscribe: () => () => {} },
  ),
  wsStartBusy: () => false,
}));

const { SettingsDialog } = await import("../SettingsDialog.tsx");
const { useSettingsUI } = await import("../store.ts");

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
  await act(async () => root!.render(<SettingsDialog />));
  await flush();
}
const selectedTab = () => document.querySelector("[role=tab][aria-selected=true]")?.id.replace(/^.*-tab-/, "");
const activeRail = () => document.querySelector(".settings-rail-item.active")?.textContent;
const entered = () => document.querySelector(".settings-layout")!.classList.contains("entered");
const open = async (s: string) => {
  act(() => useSettingsUI.getState().openSettings(s));
  await flush();
};

beforeEach(() => localStorage.clear());
afterEach(() => {
  act(() => root?.unmount());
  act(() => useSettingsUI.getState().closeSettings());
  host?.remove();
  root = host = null;
});

describe("view requests across the two memory entries", () => {
  it("lands on the named view of the other entry, in both directions", async () => {
    await open("memory:changes");
    await mount();
    expect(selectedTab()).toBe("changes");

    await open("memorysnapshots:transfer");
    expect(selectedTab()).toBe("transfer");
    expect(useSettingsUI.getState().settingsView).toBeNull();

    await open("memory:importexport");
    expect(selectedTab()).toBe("importexport");
    expect(useSettingsUI.getState().settingsView).toBeNull();
  });

  it("a view the target entry does not have is dropped there, not by the entry being left", async () => {
    await open("memory:changes");
    await mount();
    await open("memorysnapshots:nonsense");
    expect(selectedTab()).toBe("history");
    expect(useSettingsUI.getState().settingsView).toBeNull();
  });
});

describe("view requests and the phone drill-down", () => {
  it("opens straight into the section when the dialog was closed", async () => {
    await open("memory:settings");
    await mount();
    expect(entered()).toBe(true);
    expect(activeRail()).toBe("Agent memory");
  });

  it("a plain open of the section still starts at the list", async () => {
    await open("memory");
    await mount();
    expect(entered()).toBe(false);
  });

  it("drills in again for another view of the section already selected", async () => {
    await open("memory");
    await mount();
    expect(entered()).toBe(false);
    await open("memory:changes");
    expect(entered()).toBe(true);
    expect(selectedTab()).toBe("changes");

    act(() => document.querySelector<HTMLButtonElement>(".settings-back")!.click());
    expect(entered()).toBe(false);
    await open("memory:settings");
    expect(entered()).toBe(true);
    expect(selectedTab()).toBe("settings");
  });
});
