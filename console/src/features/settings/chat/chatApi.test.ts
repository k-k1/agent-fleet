import { beforeEach, describe, expect, it, vi } from "vitest";

// Runs the wrappers against a mocked client: the URL, method, body and argument count each one
// sends, and what a null body or an error body does.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./chatApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
});

const reads: [string, () => Promise<unknown>, unknown[]][] = [
];
const writes: [string, () => Promise<unknown>, unknown[]][] = [
  ["inspectDiscord", () => A.inspectDiscord("t"), ["api/connections/discord/inspect", "POST", { token: "t" }]],
  ["listDiscordGuilds", () => A.listDiscordGuilds("t"), ["api/connections/discord/guilds", "POST", { token: "t" }]],
  ["putDiscordConn", () => A.putDiscordConn({ a: 1 }), ["api/connections/discord", "PUT", { a: 1 }]],
  ["inspectSlack", () => A.inspectSlack("b", "a"), ["api/connections/slack/inspect", "POST", { botToken: "b", appToken: "a" }]],
  ["listSlackChannels", () => A.listSlackChannels("b", "e"), ["api/connections/slack/channels", "POST", { botToken: "b", email: "e" }]],
  ["putSlackConn", () => A.putSlackConn({ a: 1 }), ["api/connections/slack", "PUT", { a: 1 }]],
  ["putChatConn discord", () => A.putChatConn("discord", { n: 1 }), ["api/connections/discord", "PUT", { n: 1 }]],
  ["putChatConn slack", () => A.putChatConn("slack", { n: 1 }), ["api/connections/slack", "PUT", { n: 1 }]],
];

describe("chat wrappers send what the components used to", () => {
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
