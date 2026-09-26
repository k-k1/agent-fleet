// #972 review rounds 3–5: the Agent used to answer "recommended" (and shape the model list) from
// the ui-prefs it holds, which a settings change reaches only with the debounced PUT 600 ms later
// — or never, before the first read of the server copy or when the save fails — so answers for
// the old setting landed under the new setting's key. The Console now sends its current setting
// with the question (?hidden=), making the answer a function of the question; these pin that, and
// that an answer never crosses tenants.
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

// The Agent computes from the ?hidden= the request carries (agent_models.go
// requestHiddenModels), as the real one does. failNext makes the next GET not land (a booting
// workspace's 502).
let log: string[] = [];
let failNext = false;

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
  log = [];
  failNext = false;
  apiMock.mockReset().mockImplementation(async (p: string) => {
    if (p === "api/env/ui-prefs") return {};
    const m = /^api\/agents\/([^/?]+)\/models(?:\?(.*))?$/.exec(p);
    if (!m) return null;
    const hidden: string[] = JSON.parse(new URLSearchParams(m[2] ?? "").get("hidden") ?? "[]");
    log.push(`GET ${tenant} hidden=${hidden.filter((h) => h !== "fable").join(",")}`);
    if (failNext) {
      failNext = false;
      return null;
    }
    const short = tenant === "t2" ? "opus" : hidden.includes("haiku") ? "sonnet" : "haiku";
    const models = ["gpt-6-luna", "gpt-6-sol"].filter((id) => !hidden.includes(id)).map((id) => ({ id, label: id }));
    return { models, recommended: { chat: "sonnet", prose: "sonnet", short } };
  });
  apiJSONMock.mockReset().mockImplementation(async (p: string) => {
    if (p === "api/env/ui-prefs") log.push("PUT");
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
  it("answers for the screen's current hidden list at once, before the save lands", async () => {
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
    await flush(); // no time passes: the debounced PUT has not gone out
    expect(log).toEqual(["GET t1 hidden=", "GET t1 hidden=haiku"]);
    expect(host.textContent).toBe("sonnet");
    await flush(700);
    expect(log).toContain("PUT");
  });

  it("brings an un-hidden model back into the list at once", async () => {
    const { settings, models } = await fresh();
    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable"], codex: ["gpt-6-sol"] }));
    function Probe() {
      return <span>{(models.useModelOptions("codex") ?? []).map(([id]) => id || "default").join(",")}</span>;
    }
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("default,gpt-6-luna");
    await act(async () => settings.setSetting("hiddenModels", { claude: ["fable"], codex: [] }));
    await flush();
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
    expect(host.textContent).toBe("opus");
  });

  // #972 review round 5: a question begun under t1 and retried after a switch to t2 would be
  // sent — and answered — as t2, then kept under t1's key.
  it("drops a retry that would be asked as another tenant", async () => {
    const { models } = await fresh();
    function Probe() {
      return <span>{models.useRecommendedModels("claude")?.short ?? "-"}</span>;
    }
    failNext = true; // t1's first attempt does not land; its retry is due in 1.5 s
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("-");
    act(() => root!.unmount());
    tenant = "t2";
    await flush(60_000); // t1's retry comes due under t2
    expect(log).toEqual(["GET t1 hidden="]); // …and is not sent
    tenant = "t1";
    root = createRoot(host);
    await act(async () => root!.render(<Probe />));
    await flush();
    expect(host.textContent).toBe("haiku"); // asked afresh as t1, not t2's "opus"
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
