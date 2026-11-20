// Settings > Agents draws a harness card only for a kind the live catalogue (GET /agents/kinds)
// lists: one the policy forbids, or the member switched off, has none (ADR 0138 D10a), and with no
// answer no card is drawn. The member's own switches come back through the reset line. A deep
// link to a harness lands once the cards are drawn; one with no card falls back quietly.
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
vi.mock("../../../lib/settings.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../lib/settings.ts")>()),
  waitPrefsSaved: () => Promise.resolve(true),
}));
let wsState = "running";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));

const { AgentsTab } = await import("./AgentsTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { getSettings, setSettings, settingsDefaults } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

const ALL = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse"];
let kinds: unknown = { kinds: ALL.map((kind) => ({ kind })), access: { policyConfirmed: true } };

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
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}
const card = (id: string) => host!.querySelector<HTMLElement>(`[data-settings-target="${id}"]`);
const cards = () => Array.from(host!.querySelectorAll<HTMLElement>(".p-card")).map((c) => c.dataset.settingsTarget);

beforeEach(() => {
  wsState = "running";
  kinds = { kinds: ALL.map((kind) => ({ kind })), access: { policyConfirmed: true } };
  scrolled.length = 0;
  api.mockImplementation((path: string) => Promise.resolve(path === "api/agents/kinds" ? kinds : {}));
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
  setSettings(settingsDefaults());
});

describe("Settings > Agents", () => {
  it("unrestricted: draws every harness, with no sign-in control", async () => {
    await mount();
    expect(cards()).toEqual(ALL);
    expect(host!.querySelector("input.cinput")).toBeNull();
    expect(host!.textContent).not.toContain(t("cred.own_settings_line"));
  });

  it("lets the user turn off Codex's saved launch defaults", async () => {
    await mount();
    const codex = card("codex")!;
    const behavior = codex.querySelector<HTMLButtonElement>(".ps-disclosure")!;
    await act(async () => behavior.click());
    const row = [...codex.querySelectorAll(".ps-row")].find((r) => r.querySelector(".ps-label")?.textContent === t("agents.codex_remember_defaults"))!;
    expect(row.querySelector("button.active")?.textContent).toBe(t("common.on"));
    const off = [...row.querySelectorAll("button")].find((b) => b.textContent === t("common.off"))!;
    await act(async () => off.click());
    expect(getSettings().codexRememberDefaults).toBe(false);
  });

  it("restricted: draws no card for a harness the catalogue leaves out", async () => {
    kinds = { kinds: [{ kind: "codex" }, { kind: "opencode" }], access: { policyConfirmed: true } };
    await mount();
    expect(cards()).toEqual(["codex", "opencode"]);
    expect(host!.textContent).not.toContain(t("cred.own_settings_line"));
  });

  it("switched off: hides the harness and offers the reset, which brings it back once saved", async () => {
    setSettings({ agentEnabled: { codex: false } });
    kinds = { kinds: [{ kind: "claude" }], access: { policyConfirmed: true, someOffByOwnSettings: true } };
    await mount();
    expect(cards()).toEqual(["claude"]);
    expect(host!.textContent).toContain(t("cred.own_settings_line"));
    kinds = { kinds: [{ kind: "claude" }, { kind: "codex" }], access: { policyConfirmed: true } };
    const reset = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("cred.own_settings_reset"),
    )!;
    await act(async () => reset.click());
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(getSettings().agentEnabled).toEqual({});
    expect(cards()).toEqual(["claude", "codex"]);
  });

  it("draws no card from an Agent that does not prove its catalogue filtered", async () => {
    kinds = { kinds: ALL.map((kind) => ({ kind })) }; // no access summary: an older Agent
    await mount();
    expect(cards()).toEqual([]);
  });

  it("draws no card while the catalogue cannot be read", async () => {
    api.mockImplementation(() => Promise.resolve({ error: { code: "http_502" } }));
    await mount();
    expect(cards()).toEqual([]);
  });

  it("draws no card while the workspace is stopped", async () => {
    wsState = "stopped";
    await mount();
    expect(cards()).toEqual([]);
    expect(api).not.toHaveBeenCalled();
  });
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
});
