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
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  raw: (...args: unknown[]) => raw(...args),
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
