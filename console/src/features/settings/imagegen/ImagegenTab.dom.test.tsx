// Settings > Image generation (ADR 0138 D16). The provider order is drawn only from the Agent's
// /imagegen/status `order`, which leaves out what the credential policy forbids or cannot confirm
// (D10a); with no trustworthy answer the page shows no provider at all. Fleet fixtures are keyed
// the way a real deployment's default row is ("image", not "comfy") — a fixture spelled "comfy"
// cannot catch a reader that still expects one of the retired kind names (ADR 0082 P1).
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
let wsState = "running";
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: wsState, start: () => {} }),
  wsStartBusy: () => false,
}));

const { ImagegenTab, STATUS_REFRESH_MS } = await import("./ImagegenTab.tsx");
const { setSettings, settingsDefaults, getSettings } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const IMAGE = { id: "image", fleet: true, kind: "comfy", service: "ComfyUI" };
const AGY = { id: "agy", kind: "agy" };
const CODEX = { id: "codex", kind: "codex" };

/** A live answer: `order` as the Agent sends it (absent when the policy leaves nothing), and the
 *  ready providers. */
function respond(
  order: string[] | undefined,
  providers: Array<Record<string, unknown>>,
  orderKinds: Record<string, string> = { image: "comfy" },
) {
  api.mockImplementation((path: string) => {
    if (path === "api/imagegen/status") {
      return Promise.resolve({
        enabled: true,
        ready: providers.length > 0,
        orderFiltered: true,
        orderKinds,
        ...(order ? { order } : {}),
        providers,
      });
    }
    return Promise.resolve({});
  });
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<ImagegenTab />);
  });
  // Two microtask turns: the fetch ImagegenTab fires on mount, then the state update it queues.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const orderRows = (): string[] =>
  Array.from(host!.querySelectorAll(".ds-orderlist-row .ds-orderlist-label")).map((n) => n.textContent || "");
const text = () => host!.textContent || "";

beforeEach(() => {
  wsState = "running";
  setSettings({ ...settingsDefaults(), imageGeneration: true });
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

describe("image provider order follows the policy-filtered answer (ADR 0138 D10a)", () => {
  it("unrestricted: every provider in the Agent's order, a fleet row under its key and kind", async () => {
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Antigravity", "Codex"]);
  });

  it("keeps an allowed provider that is not ready yet", async () => {
    respond(["image", "agy", "codex"], [IMAGE]);
    await mount();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Antigravity", "Codex"]);
  });

  it("leaves out a provider the policy hides", async () => {
    respond(["image", "codex"], [IMAGE, CODEX]);
    await mount();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Codex"]);
    expect(text()).not.toContain("Antigravity");
  });

  it("shows no provider when the policy hides them all", async () => {
    respond(undefined, []);
    await mount();
    expect(orderRows()).toEqual([]);
    expect(text()).toContain(t("set.imagegen_order_none"));
    expect(text()).not.toContain("Antigravity");
    expect(text()).not.toContain("Codex");
  });

  it("shows no provider while the workspace is stopped", async () => {
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    expect(orderRows().length).toBe(3);
    wsState = "stopped";
    await act(async () => {
      root!.render(<ImagegenTab />);
    });
    expect(orderRows()).toEqual([]);
    expect(text()).toContain(t("set.imagegen_order_unavailable"));
  });

  it("shows no provider when the status cannot be read", async () => {
    api.mockImplementation(() => Promise.reject(new Error("boom")));
    await mount();
    expect(orderRows()).toEqual([]);
    expect(text()).toContain(t("set.imagegen_order_unavailable"));
  });

  // An Agent from after ADR 0082 but before ADR 0138 M1 sends the same shape, unfiltered; only
  // orderFiltered tells the two apart.
  it("shows no provider for an Agent that does not mark its order as filtered", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/imagegen/status"
          ? { enabled: true, ready: true, order: ["image", "agy", "codex"], providers: [IMAGE, AGY, CODEX] }
          : {},
      ),
    );
    await mount();
    expect(orderRows()).toEqual([]);
    expect(text()).not.toContain("Antigravity");
    expect(text()).toContain(t("set.imagegen_order_unavailable"));
  });

  // An Agent build old enough to predate `kind` (ADR 0082 P0/P1) cannot be trusted with the
  // fleet's row names either; it reads as no answer.
  it("shows no provider for an Agent that predates ADR 0082", async () => {
    respond(["codex", "comfy"], [{ id: "codex" }, { id: "image", fleet: true }]);
    await mount();
    expect(orderRows()).toEqual([]);
    expect(text()).toContain(t("set.imagegen_order_unavailable"));
  });
});

// ADR 0138 D16 moved this switch and the order off the Agents page: the controls must still write
// the same prefs (imageGeneration, imageProviderOrder), which the Agent reads through ui-prefs.
describe("Settings > Image generation writes the prefs the Agents page wrote", () => {
  const choice = (label: string, txt: string): HTMLButtonElement => {
    const row = Array.from(host!.querySelectorAll<HTMLElement>(".ds-row")).find(
      (r) => r.querySelector(".ds-label")?.textContent === label,
    );
    const b = Array.from(row?.querySelectorAll<HTMLButtonElement>(".seg-btn") ?? []).find((x) => x.textContent === txt);
    if (!b) throw new Error(`no ${txt} on ${label}`);
    return b;
  };

  it("turns image generation off and hides the order with it", async () => {
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    expect(orderRows().length).toBeGreaterThan(0);
    await act(async () => choice(t("agents.image_generation"), t("common.off")).click());
    expect(getSettings().imageGeneration).toBe(false);
    expect(orderRows()).toEqual([]);
  });

  it("stores the reordered visible list, keeping what the policy hides in its slot", async () => {
    setSettings({ ...settingsDefaults(), imageGeneration: true, imageProviderOrder: ["agy", "image", "codex"] });
    respond(["image", "codex"], [IMAGE, CODEX]);
    await mount();
    const down = host!.querySelector<HTMLButtonElement>(".ds-orderlist-row button:not([disabled])");
    if (!down) throw new Error("no order control");
    await act(async () => down.click());
    // agy keeps its slot, so it ranks first again once the policy allows it.
    expect(getSettings().imageProviderOrder).toEqual(["agy", "codex", "image"]);
    expect(orderRows()).toEqual(["Codex", "image (ComfyUI)"]);
  });
});

// A preference saved before ADR 0082 names the engine by its kind ("comfy"); the Agent shows the
// row under its key. Moving that row has to land where the member put it once the Agent reads the
// saved order again.
describe("a legacy alias in the stored order", () => {
  it("saves a reorder of the aliased row so that the Agent runs it where it was put", async () => {
    setSettings({ ...settingsDefaults(), imageGeneration: true, imageProviderOrder: ["comfy", "agy", "codex"] });
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    const downOf = (label: string) =>
      Array.from(host!.querySelectorAll<HTMLElement>(".ds-orderlist-row"))
        .find((r) => r.querySelector(".ds-orderlist-label")?.textContent === label)!
        .querySelectorAll<HTMLButtonElement>("button")[1];
    await act(async () => downOf("image (ComfyUI)").click());
    await act(async () => downOf("image (ComfyUI)").click());
    expect(orderRows()).toEqual(["Antigravity", "Codex", "image (ComfyUI)"]);
    // The alias now stands after image: it no longer pulls image back to the front.
    expect(getSettings().imageProviderOrder).toEqual(["agy", "codex", "image", "comfy"]);
  });

  it("does not list a kind name the deployment declares no row of", async () => {
    respond(["image", "openai-compat", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Antigravity", "Codex"]);
  });
});

// A policy changed while the page stays open reaches it on the next refresh (ADR 0138 D10a).
describe("a policy change while the page is open", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());
  const tick = async () => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STATUS_REFRESH_MS);
    });
  };

  it("drops a provider the policy starts to hide", async () => {
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    expect(orderRows()).toContain("Antigravity");
    respond(["image", "codex"], [IMAGE, CODEX]);
    await tick();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Codex"]);
  });

  it("brings back a provider the policy allows again", async () => {
    respond(["image", "codex"], [IMAGE, CODEX]);
    await mount();
    expect(orderRows()).not.toContain("Antigravity");
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await tick();
    expect(orderRows()).toEqual(["image (ComfyUI)", "Antigravity", "Codex"]);
  });

  it("fails closed when a refresh cannot be read", async () => {
    respond(["image", "agy", "codex"], [IMAGE, AGY, CODEX]);
    await mount();
    api.mockImplementation(() => Promise.reject(new Error("boom")));
    await tick();
    expect(orderRows()).toEqual([]);
  });
});
