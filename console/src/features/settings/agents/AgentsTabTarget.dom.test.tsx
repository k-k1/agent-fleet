// openSettings("agents", { target }) on the Agents page: its harness cards need no connection
// snapshot, so a target lands at once; a target with no card falls back to the top quietly.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
let wsState = "stopped";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));

const { AgentsTab } = await import("./AgentsTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { resetConnectionsCache } = await import("../parts/useConnections.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const scrolled: Array<{ id: string | undefined; opts: unknown }> = [];

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AgentsTab />);
  });
}
const card = (id: string) => host!.querySelector<HTMLElement>(`[data-settings-target="${id}"]`);

beforeEach(() => {
  wsState = "stopped";
  scrolled.length = 0;
  api.mockImplementation(() => Promise.resolve({}));
  Element.prototype.scrollIntoView = function (this: HTMLElement, opts?: unknown) {
    scrolled.push({ id: this.dataset.settingsTarget, opts });
  };
  window.matchMedia = ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  useSettingsUI.setState({ settingsTarget: null });
  resetConnectionsCache();
});

describe("Settings > Agents target", () => {
  it("scrolls the requested harness's card into view, flashes it and consumes the request", async () => {
    useSettingsUI.getState().openSettings("agents", { target: "codex" });
    await mount();
    expect(scrolled).toEqual([{ id: "codex", opts: { block: "start", behavior: "smooth" } }]);
    expect(card("codex")!.classList.contains("p-card-flash")).toBe(true);
    expect(card("claude")!.classList.contains("p-card-flash")).toBe(false);
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("falls back quietly to the top of the section when no such card exists", async () => {
    useSettingsUI.getState().openSettings("agents", { target: "no-such-agent" });
    await mount();
    expect(scrolled).toEqual([]);
    expect(host!.querySelector(".p-card-flash")).toBeNull();
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  // Sign-ins are on Model credentials (ADR 0138 D16): no harness card offers one here.
  it("draws no sign-in, key or endpoint control", async () => {
    wsState = "running";
    await mount();
    expect(host!.querySelectorAll(".p-card").length).toBeGreaterThanOrEqual(9);
    expect(host!.querySelector("input.cinput")).toBeNull();
    expect(host!.textContent).not.toContain("Not connected");
  });
});
