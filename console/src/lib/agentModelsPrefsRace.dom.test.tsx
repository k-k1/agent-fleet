// #972 review round 3: the Agent answers "recommended" (and shapes the model list) from the
// ui-prefs it holds, and a settings change reaches it only with the debounced PUT, 600 ms later.
// Asked the moment "hidden models" changed, it answered from the old list — and that answer was
// cached under the NEW list's key. These pin that the question waits for the save, and that an
// answer never crosses tenants.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let tenant = "t1";
const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  getTenant: () => tenant,
  getUser: () => "u1",
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
  raw: vi.fn(async () => new Response("")),
  isTransientErr: () => false,
}));

// What the Agent holds: updated by the PUT, read by the GET.
let serverHidden: string[] = [];
let log: string[] = [];

async function fresh() {
  vi.resetModules();
  const settings = await import("./settings.ts");
  const models = await import("./agentModels.ts");
  return { settings, models };
}

let root: Root | null = null;
let host: HTMLDivElement;

async function flush(ms = 0): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  tenant = "t1";
  serverHidden = [];
  log = [];
  apiMock.mockReset().mockImplementation(async (p: string) => {
    if (p === "api/env/ui-prefs") return {};
    const kind = /^api\/agents\/([^/]+)\/models$/.exec(p)?.[1];
    if (!kind) return null;
    log.push(`GET ${tenant} hidden=${serverHidden.join(",")}`);
    const short = tenant === "t2" ? "opus" : serverHidden.includes("haiku") ? "sonnet" : "haiku";
    return { models: [], recommended: { chat: "sonnet", prose: "sonnet", short } };
  });
  apiJSONMock.mockReset().mockImplementation(async (p: string, _m: string, body: any) => {
    if (p === "api/env/ui-prefs") {
      serverHidden = body?.hiddenModels?.claude ?? [];
      log.push(`PUT hidden=${serverHidden.join(",")}`);
    }
    return {};
  });
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

describe("useRecommendedModels and the ui-prefs save", () => {
  it("asks only after the hidden-models change has reached the Agent", async () => {
    const { settings, models } = await fresh();
    expect(await settings.hydrateUIPrefs()).toBe(true); // saves flow only after the server read
    function Probe() {
      return <span>{models.useRecommendedModels("claude")?.short ?? "-"}</span>;
    }
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("haiku");

    await act(async () => settings.setSetting("hiddenModels", { claude: ["haiku"] }));
    await flush();
    // Before the debounced PUT: the old answer's key is gone, and nothing was asked yet.
    expect(log.filter((l) => l.startsWith("GET")).length).toBe(1);
    await flush(700);
    expect(log.slice(1)).toEqual(["PUT hidden=haiku", "GET t1 hidden=haiku"]);
    expect(host.textContent).toBe("sonnet");
  });

  it("never serves one tenant's answer to another", async () => {
    const { models } = await fresh();
    function Probe() {
      return <span>{models.useRecommendedModels("claude")?.short ?? "-"}</span>;
    }
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("haiku");
    act(() => root!.unmount());
    tenant = "t2";
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    // Not even for one render: the first paint under t2 must not name t1's model.
    expect(host.textContent).not.toBe("haiku");
    await flush();
    expect(log).toEqual(["GET t1 hidden=", "GET t2 hidden="]);
    expect(host.textContent).toBe("opus");
  });
});

describe("fillRecommendedModelMaps", () => {
  it("gives every assistant kind an entry, never touching an explicit one", async () => {
    const { settings } = await fresh();
    const o: Record<string, unknown> = { aiShortModels: { codex: "", claude: "haiku" } };
    expect(settings.fillRecommendedModelMaps(o)).toBe(true);
    const short = o.aiShortModels as Record<string, string>;
    expect(short.codex).toBe(""); // the explicit CLI default stays
    expect(short.claude).toBe("haiku");
    for (const k of settings.ASSISTANT_AGENT_KINDS.filter((k) => k !== "codex" && k !== "claude")) {
      expect(short[k]).toBe(settings.ASSISTANT_RECOMMENDED_MODEL);
    }
    expect("aiProseModels" in o).toBe(false); // absent: DEFAULTS supply it
    expect(settings.fillRecommendedModelMaps(o)).toBe(false);
  });

  it("covers muse in the defaults, so a new member's muse row is 推奨 too", async () => {
    const { settings } = await fresh();
    const s = settings.getSettings();
    for (const key of ["assistantModels", "aiShortModels", "aiProseModels"] as const) {
      expect(s[key].muse).toBe(settings.ASSISTANT_RECOMMENDED_MODEL);
    }
  });
});
