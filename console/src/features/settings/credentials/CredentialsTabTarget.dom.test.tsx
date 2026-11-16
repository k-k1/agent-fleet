// openSettings("credentials", { target }) — the Re-authenticate deep-link: Settings > Model
// credentials scrolls the named agent's card into view and flashes it once the connection snapshot
// has drawn the cards, and falls back to the top of the section (no error) when the card is absent.
// A card is drawn only for an entry the Agent sent (ADR 0138 D10a): none while there is no answer.
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
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));
let wsState = "stopped";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));

const { CredentialsTab } = await import("./CredentialsTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { resetConnectionsCache } = await import("../parts/useConnections.ts");

// A snapshot naming every card's kind, none of them connected.
const SNAPSHOT = Object.fromEntries(
  ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse"].map((k) => [k, { connected: false }]),
);
function answer(conns: Record<string, unknown>) {
  api.mockImplementation((path: string) => Promise.resolve(path === "api/connections" ? conns : {}));
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const scrolled: Array<{ id: string | undefined; opts: unknown }> = [];

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<CredentialsTab />);
  });
}
const card = (id: string) => host!.querySelector<HTMLElement>(`[data-settings-target="${id}"]`);
const flushed = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

beforeEach(() => {
  wsState = "running";
  scrolled.length = 0;
  answer(SNAPSHOT);
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

describe("Settings > Model credentials target", () => {
  it("scrolls the requested agent's card into view, flashes it and consumes the request", async () => {
    useSettingsUI.getState().openSettings("credentials", { target: "codex" });
    await mount();
    await flushed();
    expect(scrolled).toEqual([{ id: "codex", opts: { block: "start", behavior: "smooth" } }]);
    expect(card("codex")!.classList.contains("p-card-flash")).toBe(true);
    expect(card("claude")!.classList.contains("p-card-flash")).toBe(false);
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("jumps instead of animating when the user prefers reduced motion", async () => {
    window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    useSettingsUI.getState().openSettings("credentials", { target: "claude" });
    await mount();
    await flushed();
    expect(scrolled[0].opts).toEqual({ block: "start", behavior: "auto" });
  });

  it("draws no card while the snapshot is out, and holds the deep-link until it lands", async () => {
    let release: (v: unknown) => void = () => {};
    api.mockImplementation((path: string) =>
      path === "api/connections" ? new Promise((r) => (release = r)) : Promise.resolve({}),
    );
    useSettingsUI.getState().openSettings("credentials", { target: "claude" });
    await mount();
    await flushed();
    expect(host!.querySelectorAll(".p-card").length).toBe(0);
    expect(host!.textContent).not.toContain("Not connected");
    expect(scrolled).toEqual([]);
    expect(useSettingsUI.getState().settingsTarget).toBe("claude");
    await act(async () => release(SNAPSHOT));
    await flushed();
    expect(scrolled.map((s) => s.id)).toEqual(["claude"]);
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
    expect(host!.textContent).toContain("Not connected");
  });

  it("a first fetch that fails every retry shows a retryable error and no card", async () => {
    vi.useFakeTimers();
    try {
      api.mockImplementation((path: string) =>
        path === "api/connections" ? Promise.resolve({ error: { code: "http_502" } }) : Promise.resolve({}),
      );
      await mount();
      for (let i = 0; i < 4; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1300); });
      expect(host!.textContent).not.toContain("Not connected");
      expect(host!.querySelectorAll(".p-card").length).toBe(0);
      expect(host!.textContent).toContain("Retry");
    } finally {
      vi.useRealTimers();
    }
  });

  it("draws the cards at once from the last snapshot while the refetch is still out", async () => {
    const releases: Array<(v: unknown) => void> = [];
    api.mockImplementation((path: string) =>
      path === "api/connections" ? new Promise((r) => releases.push(r)) : Promise.resolve({}),
    );
    await mount();
    await act(async () => releases.shift()!(SNAPSHOT));
    await flushed();
    expect(card("claude")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mount();
    await flushed();
    expect(releases.length).toBe(1);
    expect(card("claude")).not.toBeNull();
  });

  it("falls back quietly to the top of the section when no such card exists", async () => {
    useSettingsUI.getState().openSettings("credentials", { target: "no-such-agent" });
    await mount();
    await flushed();
    expect(scrolled).toEqual([]);
    expect(host!.querySelector(".p-card-flash")).toBeNull();
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("draws no card while the workspace is stopped", async () => {
    wsState = "stopped";
    await mount();
    await flushed();
    expect(host!.querySelectorAll(".p-card").length).toBe(0);
  });
});
