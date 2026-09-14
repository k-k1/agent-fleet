// ADR 0082 P1: the image provider order list stops being drawn from the two static kind-name
// placeholders once the Agent has answered, and draws one row per DECLARED images row instead
// (decision 3 / decision 8). This is the regression class P0 itself shipped once already
// (fleetProvider() matching the id's spelling): every fixture below is deliberately keyed the
// way a real deployment's default row is ("image", not "comfy") — a fixture spelled "comfy"
// cannot catch a reader that still expects one of the two retired kind names.
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

const { AgentsTab } = await import("./AgentsTab.tsx");
const { setSettings, settingsDefaults, IMAGE_PROVIDERS } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

/** A LIVE answer naming these fleet rows (possibly zero — a real "this deployment declares
 *  nothing" answer, distinct from noLiveAnswer() below). */
function respond(imagegenProviders: Array<Record<string, unknown>> = []) {
  api.mockImplementation((path: string) => {
    if (path === "api/imagegen/status") {
      return Promise.resolve({ enabled: true, ready: imagegenProviders.length > 0, providers: imagegenProviders });
    }
    return Promise.resolve({});
  });
}

/** No live answer at all — an Agent old enough to omit `providers`, or one this build cannot
 *  reach. This is what the fallback path is FOR, and it is a different fixture from
 *  respond([]) (a live answer that happens to be empty) on purpose (see settings.ts's
 *  normalizeImageProviderOrder `rows === undefined` vs `[]` distinction). */
function noLiveAnswer() {
  api.mockImplementation((path: string) => {
    if (path === "api/imagegen/status") return Promise.resolve({ enabled: false, ready: false });
    return Promise.resolve({});
  });
}

/** An Agent build old enough to predate `kind` (ADR 0082 P0/P1) — `providers` is present, and
 *  every entry has `fleet` (a pre-P1 Agent) or neither field at all (pre-P0), but NONE carry
 *  `kind`. This must read the same as noLiveAnswer(), not as "the fleet row is real but has no
 *  kind" — see isPreAdr0082Status. */
function oldAgentAnswer() {
  api.mockImplementation((path: string) => {
    if (path === "api/imagegen/status") {
      return Promise.resolve({ enabled: true, ready: true, providers: [{ id: "codex" }, { id: "image", fleet: true }] });
    }
    return Promise.resolve({});
  });
}

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

const orderRows = (): string[] =>
  Array.from(host!.querySelectorAll(".ds-orderlist-row .ds-orderlist-label")).map((n) => n.textContent || "");

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

describe("画像生成の優先順位（ADR 0082 P1）", () => {
  it("Agent が答える前は静的な既定（畳み込み表示）", async () => {
    noLiveAnswer();
    await mount();
    // The fallback fold: both static ids collapse into one "Agent Fleet (self-hosted)" row.
    expect(orderRows().filter((l) => l === "Agent Fleet (self-hosted)").length).toBe(1);
  });

  // Regression pinned after review of PR #658: an Agent build old enough to predate `kind`
  // still sends `providers` (with `fleet` on the pre-P1 shape, or neither field at all on
  // pre-P0), so a check that only looked at "did providers arrive at all" wrongly treated this
  // as a LIVE answer of zero fleet rows and dropped the fleet's own engine from the ordering
  // list silently — the same class of bug P0 itself shipped once already, just one layer later.
  it("kind の無い providers（ADR 0082 より前の Agent）は静的な既定に落ちる。fleet 行が消えない", async () => {
    oldAgentAnswer();
    await mount();
    expect(orderRows()).toContain("Agent Fleet (self-hosted)");
    expect(orderRows().some((l) => l.startsWith("image"))).toBe(false);
  });

  it("Agent が images 行を 1 本だけ答えたら、その行のキーで 1 行だけ出る（畳み込みではない）", async () => {
    respond([{ id: "image", fleet: true, kind: "comfy", service: "ComfyUI" }]);
    await mount();
    const rows = orderRows();
    expect(rows.some((l) => l.startsWith("image"))).toBe(true);
    expect(rows).not.toContain("Agent Fleet (self-hosted)");
  });

  it("images 行が 2 本なら、両方が別々の行として、キーで出る", async () => {
    respond([
      { id: "image", fleet: true, kind: "comfy", service: "ComfyUI" },
      { id: "comfy-lan", fleet: true, kind: "comfy", service: "ComfyUI" },
    ]);
    await mount();
    const rows = orderRows();
    expect(rows.some((l) => l.startsWith("image"))).toBe(true);
    expect(rows.some((l) => l.startsWith("comfy-lan"))).toBe(true);
    expect(rows.length).toBe(4); // the two fleet rows + agy + codex
  });

  it("Agent が「行は 0 本」と答えたら、フォールバックの畳み込み行は出さない（あるのはベンダー2行だけ）", async () => {
    // A REAL live answer that happens to be empty — a deployment with no image engine at all —
    // is a different fact from "no answer yet", and must NOT fall back to the two static
    // placeholders: that would offer a rank nothing can ever route to (see settings.ts).
    respond([]);
    await mount();
    expect(orderRows()).not.toContain("Agent Fleet (self-hosted)");
    expect(orderRows().length).toBe(2); // agy + codex, no fleet row of any kind
  });

  it("ワークスペース停止中は静的な既定に戻る（残っていた行は見せない）", async () => {
    respond([{ id: "image", fleet: true, kind: "comfy", service: "ComfyUI" }]);
    await mount();
    expect(orderRows().some((l) => l.startsWith("image"))).toBe(true);

    wsState = "stopped";
    await act(async () => {
      root!.render(<AgentsTab />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(orderRows()).toContain("Agent Fleet (self-hosted)");
  });
});
