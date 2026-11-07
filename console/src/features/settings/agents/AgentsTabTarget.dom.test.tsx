// openSettings("agents", { target }) — the Re-authenticate deep-link: Settings > Agents scrolls the
// named agent's card into view and flashes it, waits for cards that mount after the connection
// snapshot loads, and falls back to the top of the section (no error) when the card is absent.
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
const flushed = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

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
  it("scrolls the requested agent's card into view, flashes it and consumes the request", async () => {
    useSettingsUI.getState().openSettings("agents", { target: "codex" });
    await mount();
    expect(scrolled).toEqual([{ id: "codex", opts: { block: "start", behavior: "smooth" } }]);
    expect(card("codex")!.classList.contains("p-card-flash")).toBe(true);
    expect(card("claude")!.classList.contains("p-card-flash")).toBe(false);
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("jumps instead of animating when the user prefers reduced motion", async () => {
    window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    useSettingsUI.getState().openSettings("agents", { target: "claude" });
    await mount();
    expect(scrolled[0].opts).toEqual({ block: "start", behavior: "auto" });
  });

  it("draws every card with a checking chip while the snapshot is out, and holds the deep-link until the snapshot lands", async () => {
    wsState = "running";
    let release: (v: unknown) => void = () => {};
    api.mockImplementation((path: string) =>
      path === "api/connections" ? new Promise((r) => (release = r)) : Promise.resolve({}),
    );
    useSettingsUI.getState().openSettings("agents", { target: "claude" });
    await mount();
    await flushed();
    expect(card("claude")).not.toBeNull();
    expect(host!.textContent).not.toContain("Loading");
    // Every card says "checking", none claims "not connected" while the probe is out.
    expect(host!.querySelectorAll(".p-card").length).toBeGreaterThanOrEqual(9);
    expect(host!.querySelectorAll(".p-status").length).toBe(host!.querySelectorAll(".p-card").length);
    expect(Array.from(host!.querySelectorAll(".p-status")).every((e) => e.textContent === "Checking…")).toBe(true);
    expect(host!.textContent).not.toContain("Not connected");
    // The deep link waits: the snapshot adds login fields that change card heights.
    expect(scrolled).toEqual([]);
    expect(useSettingsUI.getState().settingsTarget).toBe("claude");
    await act(async () => release({}));
    await flushed();
    expect(scrolled.map((s) => s.id)).toEqual(["claude"]);
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
    expect(host!.textContent).not.toContain("Checking");
    expect(host!.textContent).toContain("Not connected");
  });

  it("a first fetch that fails every retry shows a retryable error, never 'not connected'", async () => {
    vi.useFakeTimers();
    try {
      wsState = "running";
      api.mockImplementation((path: string) =>
        path === "api/connections" ? Promise.resolve({ error: { code: "http_502" } }) : Promise.resolve({}),
      );
      await mount();
      for (let i = 0; i < 4; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1300); });
      expect(host!.textContent).not.toContain("Not connected");
      expect(host!.textContent).toContain("Status unknown");
      expect(host!.textContent).toContain("Retry");
    } finally {
      vi.useRealTimers();
    }
  });

  it("draws the cards at once from the last snapshot while the refetch is still out", async () => {
    wsState = "running";
    const releases: Array<(v: unknown) => void> = [];
    api.mockImplementation((path: string) =>
      path === "api/connections" ? new Promise((r) => releases.push(r)) : Promise.resolve({}),
    );
    await mount();
    await act(async () => releases.shift()!({}));
    await flushed();
    expect(card("claude")).not.toBeNull();
    // Reopen: the second mount must not wait for its own GET /connections.
    act(() => root?.unmount());
    host?.remove();
    await mount();
    await flushed();
    expect(releases.length).toBe(1); // the refetch is out and unanswered
    expect(card("claude")).not.toBeNull();
    expect(host!.textContent).not.toContain("Loading");
  });

  it("falls back quietly to the top of the section when no such card exists", async () => {
    useSettingsUI.getState().openSettings("agents", { target: "no-such-agent" });
    await mount();
    expect(scrolled).toEqual([]);
    expect(host!.querySelector(".p-card-flash")).toBeNull();
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });
});
