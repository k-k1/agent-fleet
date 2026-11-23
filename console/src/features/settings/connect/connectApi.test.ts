import { beforeEach, describe, expect, it, vi } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// The connect area's typed API layer (#4783 part 5): the URL, method and body each wrapper sends,
// and the source pins that keep components from going back to a raw api() call.
const { api, apiJSON, raw } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn(), raw: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
  raw: (...a: unknown[]) => raw(...a),
}));

import * as A from "./connectApi.ts";
import { hasCodeLiteral, importsRawClient } from "../../../test/sourcePins.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
  raw.mockReset().mockResolvedValue({ ok: true });
});

describe("connectApi wrappers added for the tracker, token and connected-app screens", () => {
  it("reads", async () => {
    const cases: [() => Promise<unknown>, unknown[]][] = [
      [() => A.listPats(), ["api/pat"]],
      [() => A.listMcpOAuthGrants(), ["api/mcp-oauth/grants"]],
      [() => A.getMcpCalls("api/pat/p%2F1/calls"), ["api/pat/p%2F1/calls"]],
      [() => A.getConnections(), ["api/connections"]],
      [() => A.getGitOAuthAvailability(), ["api/git-oauth"]],
    ];
    for (const [call, args] of cases) {
      api.mockClear();
      await call();
      expect(api.mock.calls).toEqual([args]);
    }
  });

  it("writes", async () => {
    const cases: [() => Promise<unknown>, unknown[]][] = [
      [() => A.startJiraOAuth(), ["api/connections/jira/oauth/start", "POST"]],
      [() => A.putJiraToken({ site: "s", email: "e", token: "t" }), ["api/connections/jira", "PUT", { site: "s", email: "e", token: "t" }]],
      [() => A.putJiraSite("c1"), ["api/connections/jira/site", "PUT", { cloudId: "c1" }]],
      [
        () => A.createPat({ name: "n", scope: "write", ttl_days: 90, endpoint: "mcp" }),
        ["api/pat", "POST", { name: "n", scope: "write", ttl_days: 90, endpoint: "mcp" }],
      ],
    ];
    for (const [call, args] of cases) {
      apiJSON.mockClear();
      await call();
      expect(apiJSON.mock.calls).toEqual([args]);
    }
  });

  it("deletes go through raw with an encoded id and no body", async () => {
    await A.deletePat("a/b");
    await A.revokeMcpOAuthGrant("g/1");
    await A.deleteJira();
    expect(raw.mock.calls).toEqual([
      ["api/pat/a%2Fb", { method: "DELETE" }],
      ["api/mcp-oauth/grants/g%2F1", { method: "DELETE" }],
      ["api/connections/jira", { method: "DELETE" }],
    ]);
  });

  it("hands a null body through untouched", async () => {
    api.mockResolvedValue(null);
    expect(await A.listPats()).toBeNull();
    expect(await A.listMcpOAuthGrants()).toBeNull();
  });
});

describe("connect typed API layer: source pins", () => {
  const dir = fileURLToPath(new URL("./", import.meta.url));
  const read = (name: string) => readFileSync(dir + name, "utf8");
  const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && f !== "connectApi.ts");

  it("no component in the area imports api() / apiJSON() from the client", () => {
    expect(files.length).toBeGreaterThan(5);
    const offenders = files.filter((f) =>
      importsRawClient(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps the new paths literal in connectApi.ts", () => {
    const src = read("connectApi.ts");
    for (const p of [
      '"api/connections/jira/oauth/start"',
      '"api/connections/jira"',
      '"api/connections/jira/site"',
      '"api/pat"',
      "api/pat/${encodeURIComponent(id)}",
      '"api/mcp-oauth/grants"',
      "api/mcp-oauth/grants/${encodeURIComponent(id)}",
    ]) {
      expect(hasCodeLiteral(src, p)).toBe(true);
    }
  });
});
