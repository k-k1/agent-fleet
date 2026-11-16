// llama.cpp's card on the Agents page (ADR 0138 D16): the on/off switch and the behaviour
// disclosure, whose default-model row reads lcpp's live catalog.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
const raw = vi.fn();
// The Behavior disclosure's launch-defaults row (AgentCardParts.tsx's LaunchDefaults) is the
// only thing here that calls the plain GET `api` — for lcpp's live model catalog. Overridden
// completely (nothing else in this component touches it) so opening that section never makes a
// real network call.
let lcppModels: { id: string; label: string }[] = [];
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  raw: (...args: unknown[]) => raw(...args),
  api: async (path: string) => (path.includes("agents/lcpp/models") ? { models: lcppModels } : {}),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
// The connected branch renders DisconnectButton, which asks for confirmation through context.
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { LcppBehavior } = await import("./AgentBehaviors.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(_st?: undefined) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<LcppBehavior />);
  });
}

beforeEach(() => {
  apiJSON.mockImplementation(() => Promise.resolve({ ok: true, build_info: "b1", n_ctx: 4096, models: ["m1"] }));
  raw.mockImplementation(() => Promise.resolve({}));
  lcppModels = [];
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

// The Behavior disclosure's "Default model" row (docs/log/109): lcpp has no CLI-picked own
// default, so a stored default of "" is not a valid choice the way it is for codex/opencode —
// this row uses Choice/Select directly rather than ModelPicker, so it needs its own
// useAutoConcreteModel call (AgentCardParts.tsx), separate from the launch dialogs'.
describe("LcppBehavior — Behavior disclosure's default-model row auto-picks (docs/log/109)", () => {
  it("auto-picks the sole catalog entry as the stored default once it resolves", async () => {
    lcppModels = [{ id: "m1", label: "m1" }];
    const { getSettings, setSettings } = await import("../../../lib/settings.ts");
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, lcpp: { model: "", effort: "", startMode: "normal", skipPermissions: true } } });

    await mount(undefined);
    const disclosure = [...(host?.querySelectorAll("button") ?? [])].find((b) => /behavior/i.test(b.textContent || ""));
    expect(disclosure).toBeTruthy();
    await act(async () => {
      disclosure!.click();
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getSettings().agentLaunchDefaults.lcpp?.model).toBe("m1");
  });
});

describe("LcppBehavior — layout", () => {
  it("keeps the enable switch inside .p-body", async () => {
    await mount();
    expect(host!.querySelector(".p-body .ps-row")).not.toBeNull();
  });
});

// An empty catalog (no engine reachable) used to draw a segmented control with no segments —
// a bare frame next to "Default model". It must say there is nothing to choose instead.
describe("LcppBehavior — default-model row with an empty catalog", () => {
  it("shows a note instead of an empty control", async () => {
    lcppModels = [];
    const { t } = await import("../../../lib/i18n/index.ts");
    await mount(undefined);
    const disclosure = [...(host?.querySelectorAll("button") ?? [])].find((b) => /behavior/i.test(b.textContent || ""));
    await act(async () => {
      disclosure!.click();
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    const row = [...host!.querySelectorAll(".p-settings .ps-row")].find((r) =>
      r.querySelector(".ps-label")?.textContent?.startsWith(t("agents.default_model")),
    )!;
    expect(row.textContent).toContain(t("agents.default_model_none"));
    expect(row.querySelector(".choice-seg")).toBeNull();
  });
});
