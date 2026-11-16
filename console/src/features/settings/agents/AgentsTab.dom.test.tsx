// The Agents page holds harnesses only (ADR 0138 D16): the session block and image generation
// have their own pages (SessionsTab, ImagegenTab) and must not be drawn here as well.
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
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));
let wsState = "running";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));

const { AgentsTab } = await import("./AgentsTab.tsx");
const { setSettings, settingsDefaults, IMAGE_PROVIDERS } = await import(
  "../../../lib/settings.ts"
);
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AgentsTab />);
  });
  // Two microtask turns: the fetches AgentsTab fires on mount, then the state update they queue.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  wsState = "running";
  setSettings({ ...settingsDefaults(), imageGeneration: true, imageProviderOrder: [...IMAGE_PROVIDERS] });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  setSettings(settingsDefaults());
});

describe("Agents page (harness only)", () => {
  it("draws neither the session rows nor image generation", async () => {
    const all = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse"];
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/agents/kinds" ? { kinds: all.map((kind) => ({ kind })), access: { policyConfirmed: true } } : {}),
    );
    await mount();
    expect(host!.querySelectorAll(".p-card").length).toBe(all.length);
    const labels = Array.from(host!.querySelectorAll(".ds-label")).map((l) => l.textContent);
    for (const k of [
      "agents.stopped_archive",
      "agents.spend_cap_default",
      "agents.peer_messaging",
      "agents.fleet_spawn",
      "agents.pr_feedback",
      "agents.session_search",
      "agents.image_generation",
      "agents.image_provider_order",
    ] as const) {
      expect(labels).not.toContain(t(k));
    }
    expect(host!.querySelector(".ds-orderlist")).toBeNull();
    // Nothing here reads the image providers any more.
    expect(api.mock.calls.map(([p]) => p)).not.toContain("api/imagegen/status");
  });
});
