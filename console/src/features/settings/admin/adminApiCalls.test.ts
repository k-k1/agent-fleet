import { beforeEach, describe, expect, it, vi } from "vitest";

// Runs the wrappers against a mocked client: the URL, method, body and argument count each one
// sends (a DELETE must not grow a body argument), and what comes back for a null body.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./adminApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
});

const E = "api/admin/engines/a%2Fb";
const writes: [string, () => Promise<unknown>, unknown[]][] = [
  ["putBrand", () => A.putBrand({ color: "teal", label: "x" }), ["api/admin/brand", "PUT", { color: "teal", label: "x" }]],
  ["deleteBrand", () => A.deleteBrand(), ["api/admin/brand", "DELETE"]],
  ["putTtsMode", () => A.putTtsMode("on"), ["api/admin/tts", "PUT", { mode: "on" }]],
  ["putTtsDict", () => A.putTtsDict("d"), ["api/admin/tts/dict", "PUT", { dict: "d" }]],
  ["putDevIdp", () => A.putDevIdp(true), ["api/admin/dev-idp", "PUT", { enabled: true }]],
  ["deleteMcpTrusted", () => A.deleteMcpTrusted(), ["api/admin/mcp-oauth/trusted-hosts", "DELETE"]],
  ["putEgressMode", () => A.putEgressMode(true), ["api/admin/egress/mode", "PUT", { enforce: true }]],
  ["addEgressEntry", () => A.addEgressEntry("h", "r"), ["api/admin/egress/allowlist", "POST", { entry: "h", reason: "r" }]],
  ["setEgressEntryState", () => A.setEgressEntryState("i/d", "active"), ["api/admin/egress/allowlist/i%2Fd/state", "POST", { state: "active" }]],
  ["putEngineMode", () => A.putEngineMode("a/b", "on"), [E, "PUT", { mode: "on" }]],
  ["replaceEngineBox", () => A.replaceEngineBox("a/b"), [E + "/replace-box", "POST", {}]],
  ["putEngineClass", () => A.putEngineClass("a/b", "g"), [E + "/class", "PUT", { class: "g" }]],
  ["putEngineSpot", () => A.putEngineSpot("a/b", true), [E + "/spot", "PUT", { spot: true }]],
  ["putEngineNegative", () => A.putEngineNegative("a/b", "n"), [E + "/negative", "PUT", { negative: "n" }]],
  ["putEngineIdle", () => A.putEngineIdle("a/b", 30), [E + "/idle", "PUT", { idle_secs: 30 }]],
  ["discoverEngine", () => A.discoverEngine("a/b"), [E + "/discover", "POST", {}]],
  ["dismissIngestJob", () => A.dismissIngestJob("a/b", "j/1"), [E + "/ingest/j%2F1", "DELETE"]],
  ["fillEngineModelMeta", () => A.fillEngineModelMeta("a/b", "m/1"), [E + "/models/m%2F1/meta", "POST"]],
  ["callEngineModel purge", () => A.callEngineModel("a/b", "m", "DELETE", undefined, true), [E + "/models/m?purge=1", "DELETE", undefined]],
  ["issueEngineToken", () => A.issueEngineToken("t", "k"), ["api/admin/engines/issue-token", "POST", { tenant_slug: "t", user_key: "k" }]],
  ["putHfToken", () => A.putHfToken("x"), ["api/admin/engines/hf-token", "PUT", { token: "x" }]],
  ["deleteCivitaiToken", () => A.deleteCivitaiToken(), ["api/admin/engines/civitai-token", "DELETE"]],
  ["sendComfyLan DELETE", () => A.sendComfyLan("DELETE"), ["api/admin/engines/comfy-lan", "DELETE", undefined]],
  ["searchEngineCatalog", () => A.searchEngineCatalog("gguf", { q: "x" }), ["api/admin/engines/search?kind=gguf", "POST", { q: "x" }]],
  ["getIngestFiles", () => A.getIngestFiles("a/b", { hf: { repo: "r" } }), [E + "/ingest/files", "POST", { source: { hf: { repo: "r" } } }]],
  ["putProvidedDeployment", () => A.putProvidedDeployment({ a: 1 }), ["api/admin/provided-routes/deployment", "PUT", { a: 1 }]],
];

describe("admin wrappers send what the components used to", () => {
  it.each(writes)("%s", async (_n, call, want) => {
    await call();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0]).toEqual(want);
    expect(apiJSON.mock.calls[0]).toHaveLength(want.length);
  });

  it("reads hit the literal paths", async () => {
    await A.getEngineHourly("a/b", "from=1&to=2");
    await A.getEngineObjects(E + "/objects");
    await A.getRoutePolicyTenantCounts();
    expect(api.mock.calls.map((c) => c[0])).toEqual([E + "/hourly?from=1&to=2", E + "/objects", "api/admin/route-policy/tenant-counts"]);
  });

  it("folds a null body to {} where the caller reads optional fields, and keeps null elsewhere", async () => {
    apiJSON.mockResolvedValue(null);
    api.mockResolvedValue(null);
    expect(await A.putEngineSpot("a/b", true)).toEqual({});
    expect(await A.getHfToken()).toEqual({});
    expect(await A.putBrand({ color: "c", label: "l" })).toBeNull();
    expect(await A.getTts()).toBeNull();
  });

  it("hands an error body through unchanged", async () => {
    apiJSON.mockResolvedValue({ error: { code: "x" } });
    expect(await A.putEngineIdle("a/b", 1)).toEqual({ error: { code: "x" } });
  });
});
