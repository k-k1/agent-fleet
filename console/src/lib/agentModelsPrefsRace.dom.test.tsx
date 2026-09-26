// #972 review rounds 3–4: the Agent answers "recommended" (and shapes the model list) from the
// ui-prefs it holds, and a settings change reaches it only with the debounced PUT, 600 ms later
// — or never, before the first read of the server copy or when the save fails. Asked the moment
// "hidden models" changed, it answered from the old list, and that answer was cached under the
// NEW list's key. The Agent now says which list it answered under (`appliedHidden`); these pin
// that only a matching answer is kept, and that an answer never crosses tenants.
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

// What the Agent holds: updated by the PUT, read by the GET. null = never saved, which the Agent
// reads as the Console's own default (claude: fable) — sessionx.HiddenModelsRaw.
let serverHiddenMap: Record<string, string[]> | null = null;
const agentHidden = (kind: string): string[] =>
  serverHiddenMap ? (serverHiddenMap[kind] ?? []) : kind === "claude" ? ["fable"] : [];
let log: string[] = [];
let failPut = false;

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
  serverHiddenMap = null;
  log = [];
  failPut = false;
  apiMock.mockReset().mockImplementation(async (p: string) => {
    if (p === "api/env/ui-prefs") return {};
    const kind = /^api\/agents\/([^/]+)\/models$/.exec(p)?.[1];
    if (!kind) return null;
    const hidden = agentHidden(kind);
    log.push(`GET ${tenant} hidden=${hidden.filter((h) => h !== "fable").join(",")}`);
    const short = tenant === "t2" ? "opus" : hidden.includes("haiku") ? "sonnet" : "haiku";
    const models = ["gpt-6-luna", "gpt-6-sol"].filter((m) => !hidden.includes(m)).map((id) => ({ id, label: id }));
    return { models, recommended: { chat: "sonnet", prose: "sonnet", short }, appliedHidden: hidden };
  });
  apiJSONMock.mockReset().mockImplementation(async (p: string, _m: string, body: any) => {
    if (p === "api/env/ui-prefs") {
      if (failPut) {
        log.push("PUT failed");
        return { error: { code: "http_413", message: "too large" } };
      }
      const saved: Record<string, string[]> = body?.hiddenModels ?? {};
      serverHiddenMap = saved;
      const changed = Object.values(saved).flat().filter((h) => h !== "fable");
      log.push(`PUT hidden=${changed.join(",")}`);
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
  it("keeps no answer computed before the hidden-models change reached the Agent", async () => {
    const { settings, models } = await fresh();
    expect(await settings.hydrateUIPrefs()).toBe(true); // saves flow only after the server read
    function Probe() {
      return <span>{models.useRecommendedModels("claude")?.short ?? "-"}</span>;
    }
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("haiku");

    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable", "haiku"] }));
    await flush();
    // Asked at once and answered under the OLD list: not taken, so nothing is named yet.
    expect(log.slice(1)).toEqual(["GET t1 hidden="]);
    expect(host.textContent).toBe("-");
    await flush(2_000); // the save lands at 600 ms, the retry at 1.5 s
    expect(log.slice(2)).toEqual(["PUT hidden=haiku", "GET t1 hidden=haiku"]);
    expect(host.textContent).toBe("sonnet");
  });

  it("names no model while the Agent never gets the change (a failing save)", async () => {
    const { settings, models } = await fresh();
    expect(await settings.hydrateUIPrefs()).toBe(true);
    failPut = true;
    function Probe() {
      return <span>{models.useRecommendedModels("claude")?.short ?? "-"}</span>;
    }
    root = createRoot(host);
    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable", "haiku"] }));
    await act(async () => root!.render(<Probe />));
    await flush(60_000); // every retry spent
    expect(log).toContain("PUT failed");
    // Every answer was the Agent's "haiku", computed under a list the screen no longer holds.
    expect(host.textContent).toBe("-");
  });

  it("brings an un-hidden model back into the list once the Agent has the change", async () => {
    const { settings, models } = await fresh();
    expect(await settings.hydrateUIPrefs()).toBe(true);
    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable"], codex: ["gpt-6-sol"] }));
    await flush(2_000);
    function Probe() {
      return <span>{(models.useModelOptions("codex") ?? []).map(([id]) => id || "default").join(",")}</span>;
    }
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("default,gpt-6-luna");
    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable"], codex: [] }));
    await flush(2_000);
    expect(host.textContent).toBe("default,gpt-6-luna,gpt-6-sol");
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
