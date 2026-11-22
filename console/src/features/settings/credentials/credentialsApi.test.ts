import { beforeEach, describe, expect, it, vi } from "vitest";

// Runs the wrappers against a mocked client: the URL, method, body and argument count each one
// sends, and what a null body or an error body does.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./credentialsApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
});

const post = { method: "POST" };

const reads: [string, () => Promise<unknown>, unknown[]][] = [
  ["getUnusableConnections", () => A.getUnusableConnections(), ["api/connections/unusable"]],
  ["getAgentKinds", () => A.getAgentKinds(), ["api/agents/kinds"]],
  ["startClaudeLogin", () => A.startClaudeLogin(), ["api/connections/claude/start", post]],
  ["startMuseLogin", () => A.startMuseLogin(), ["api/connections/muse/start", post]],
  ["startCodexDeviceLogin", () => A.startCodexDeviceLogin(), ["api/connections/codex/device/start", post]],
  ["startKiroLogin", () => A.startKiroLogin(), ["api/connections/kiro/start", post]],
  ["startCursorLogin", () => A.startCursorLogin(), ["api/connections/cursor/start", post]],
  ["startOpencodeOauth", () => A.startOpencodeOauth(), ["api/connections/opencode/oauth/start", post]],
];
const writes: [string, () => Promise<unknown>, unknown[]][] = [
  ["completeClaudeLogin", () => A.completeClaudeLogin("f", "c"), ["api/connections/claude/complete", "POST", { flow_id: "f", code: "c" }]],
  ["startAgyLogin", () => A.startAgyLogin("m"), ["api/connections/agy/start", "POST", { method: "m" }]],
  ["completeAgyLogin", () => A.completeAgyLogin("f", "c"), ["api/connections/agy/complete", "POST", { flow_id: "f", code: "c" }]],
  ["pollMuseLogin", () => A.pollMuseLogin("f"), ["api/connections/muse/poll", "POST", { flow_id: "f" }]],
  ["putMuseApiKey", () => A.putMuseApiKey("k"), ["api/connections/muse/api-key", "POST", { key: "k" }]],
  ["pollCodexDeviceLogin", () => A.pollCodexDeviceLogin("f"), ["api/connections/codex/device/poll", "POST", { flow_id: "f" }]],
  ["putCodexApiKey", () => A.putCodexApiKey("k"), ["api/connections/codex/api-key", "POST", { key: "k" }]],
  ["pollKiroLogin", () => A.pollKiroLogin("f"), ["api/connections/kiro/poll", "POST", { flow_id: "f" }]],
  ["pollCursorLogin", () => A.pollCursorLogin("f"), ["api/connections/cursor/poll", "POST", { flow_id: "f" }]],
  ["putOpencodeKey", () => A.putOpencodeKey("E", "k"), ["api/connections/opencode", "PUT", { env: "E", key: "k" }]],
  ["restartOpencodeServe", () => A.restartOpencodeServe(), ["api/connections/opencode/serve/restart", "POST", {}]],
  ["putOpencodeWorkspace", () => A.putOpencodeWorkspace("w"), ["api/connections/opencode/workspace", "PUT", { id: "w" }]],
  ["pollOpencodeOauth", () => A.pollOpencodeOauth("f"), ["api/connections/opencode/oauth/poll", "POST", { flow_id: "f" }]],
  ["cancelOpencodeOauth", () => A.cancelOpencodeOauth("f"), ["api/connections/opencode/oauth/cancel", "POST", { flow_id: "f" }]],
  ["putLcppConnection", () => A.putLcppConnection("u", "k"), ["api/connections/lcpp", "PUT", { url: "u", apiKey: "k" }]],
  ["checkLcppConnection", () => A.checkLcppConnection(), ["api/connections/lcpp/check", "POST", {}]],
  ["removeUnusableConnections", () => A.removeUnusableConnections("t"), ["api/connections/unusable/remove", "POST", { token: "t" }]],
];

describe("credentials wrappers send what the components used to", () => {
  it.each(reads)("%s", async (_n, call, args) => {
    await call();
    expect(apiJSON).not.toHaveBeenCalled();
    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.calls[0]).toEqual(args);
    expect(api.mock.calls[0].length).toBe(args.length);
  });

  it.each(writes)("%s", async (_n, call, args) => {
    await call();
    expect(api).not.toHaveBeenCalled();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0]).toEqual(args);
    expect(apiJSON.mock.calls[0].length).toBe(args.length);
  });

  it("keeps a null body as null", async () => {
    api.mockResolvedValue(null);
    apiJSON.mockResolvedValue(null);
    for (const [, call] of [...reads, ...writes]) expect(await call()).toBeNull();
  });

  it("hands an error body through unchanged", async () => {
    api.mockResolvedValue({ error: { code: "x" } });
    apiJSON.mockResolvedValue({ error: { code: "x" } });
    for (const [, call] of [...reads, ...writes]) expect(await call()).toEqual({ error: { code: "x" } });
  });
});
