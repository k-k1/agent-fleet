// LcppCard's "reachable" follow-up (docs/log/107). Three facts have to hold, and each is easy
// to get wrong without anything looking broken:
//   1. No connection saved: the card renders exactly as it did before this feature existed.
//   2. A connection saved but never observed (`st.reachable === undefined`) auto-checks EXACTLY
//      ONCE — not on every re-render, and not again once the observation is known.
//   3. A connection already known to be reachable/unreachable shows that fact WITHOUT dialing
//      again — reopening the settings modal must not re-check a LAN box that already answered.
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

const { LcppCard } = await import("./LcppCard.tsx");
import type { ProviderConn } from "../../../types/session.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const text = () => host?.textContent || "";
const checkCalls = () => apiJSON.mock.calls.filter((c) => c[0] === "api/connections/lcpp/check");

async function mount(st: ProviderConn | undefined, reload = vi.fn()) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<LcppCard running st={st} reload={reload} />);
  });
  // Let the auto-check effect's promise chain (if it fired) settle.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  return reload;
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

describe("LcppCard — no connection saved", () => {
  it("renders the same as before this feature existed: no reachable line, no auto-check", async () => {
    await mount({ enabled: true, connected: false });
    expect(checkCalls().length).toBe(0);
    expect(text()).not.toMatch(/届いています|届いていません|Reachable|Not reachable/);
  });
});

describe("LcppCard — a connection saved but never observed", () => {
  it("auto-checks exactly once", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931" }); // reachable: undefined
    expect(checkCalls().length).toBe(1);
  });

  // Positive control for the guard: re-rendering the SAME unknown state (e.g. a parent
  // re-render that hasn't yet delivered the fresh `reachable`) must not fire a second dial.
  it("does not fire a second dial on a re-render that still reports reachable=undefined", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    const reload = vi.fn();
    const st: ProviderConn = { enabled: true, connected: true, url: "http://box:9931" };
    await act(async () => {
      root!.render(<LcppCard running st={st} reload={reload} />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      root!.render(<LcppCard running st={{ ...st }} reload={reload} />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(checkCalls().length).toBe(1);
  });
});

describe("LcppCard — a connection already known to be reachable/unreachable", () => {
  it("shows reachable=true WITHOUT dialing again", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: true });
    expect(checkCalls().length).toBe(0);
    expect(text()).toMatch(/届いています|Reachable/);
  });

  it("shows reachable=false WITHOUT dialing again, and distinctly from 'unknown'", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: false });
    expect(checkCalls().length).toBe(0);
    expect(text()).toMatch(/届いていません|Not reachable/);
  });
});

describe("LcppCard — the model behind the reachable observation (docs/log/107, 2026-09-21 addendum)", () => {
  // A member can swap the LAN box under the same saved URL, and a single-model llama-server
  // does not read the request's own `model` field — so this line is the only thing that can
  // catch a swap. It must show WITHOUT dialing again, same as the reachable line itself.
  it("shows a single observed model next to 'reachable'", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: true, model: "gemma-4-12b-it-q4_k_m" });
    expect(checkCalls().length).toBe(0);
    expect(text()).toContain("gemma-4-12b-it-q4_k_m");
  });

  it("shows a count when the observation found more than one model (a router)", async () => {
    await mount({
      enabled: true,
      connected: true,
      url: "http://box:9931",
      reachable: true,
      model: "gemma-4-12b-it-q4_k_m",
      model_count: 3,
    });
    expect(text()).toMatch(/ほか 2 件|\+2 more/);
  });

  // Positive control for the count guard: a SINGLE model must not draw a "+0 more"/"ほか 0 件".
  it("draws no count suffix for a single model", async () => {
    await mount({
      enabled: true,
      connected: true,
      url: "http://box:9931",
      reachable: true,
      model: "gemma-4-12b-it-q4_k_m",
      model_count: 1,
    });
    expect(text()).not.toMatch(/ほか|more/);
  });

  // No model observed yet: nothing model-shaped renders, and reachable itself still does.
  it("omits the model line entirely when unknown", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: true });
    expect(text()).toMatch(/届いています|Reachable/);
    expect(text()).not.toMatch(/モデル|Model:/);
  });

  // A model name must not be attributed to an UNREACHABLE box — an observation that failed
  // carries no model (engines.go's lcppMemberRecordModel clears it), but this pins the display
  // side too: even if `model` somehow rode along on the wire, unreachable must not show it.
  it("does not show a model name when unreachable", async () => {
    await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: false, model: "stale-name" });
    expect(text()).not.toContain("stale-name");
  });
});

describe("LcppCard — the manual check button still works", () => {
  it("pressing it dials once and reloads", async () => {
    const reload = await mount({ enabled: true, connected: true, url: "http://box:9931", reachable: true });
    reload.mockClear();
    apiJSON.mockClear();
    const btn = [...(host?.querySelectorAll("button") ?? [])].find((b) => /接続を確認|Check connection/.test(b.textContent || ""));
    expect(btn).toBeTruthy();
    await act(async () => {
      btn!.click();
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(checkCalls().length).toBe(1);
    expect(reload).toHaveBeenCalled();
  });
});

// The Behavior disclosure's "Default model" row (docs/log/109): lcpp has no CLI-picked own
// default, so a stored default of "" is not a valid choice the way it is for codex/opencode —
// this row uses Choice/Select directly rather than ModelPicker, so it needs its own
// useAutoConcreteModel call (AgentCardParts.tsx), separate from the launch dialogs'.
describe("LcppCard — Behavior disclosure's default-model row auto-picks (docs/log/109)", () => {
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
