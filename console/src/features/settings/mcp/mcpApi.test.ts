import { beforeEach, describe, expect, it, vi } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// The MCP area's typed API layer (#4783 part 5): URL, method and body of each wrapper, and the
// source pins that keep components from going back to a raw api() call.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./mcpApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ servers: [] });
  apiJSON.mockReset().mockResolvedValue({});
});

describe("mcpApi wrappers", () => {
  it("reads", async () => {
    const cases: [() => Promise<unknown>, unknown[]][] = [
      [() => A.getMcpRegistry(), ["api/mcp-servers"]],
      [() => A.listTenantMcpServers("a&b"), ["api/admin/mcp-servers?tenant=a%26b"]],
      [() => A.checkEgress("hosts=x"), ["api/egress/check?hosts=x"]],
    ];
    for (const [call, args] of cases) {
      api.mockClear();
      await call();
      expect(api.mock.calls).toEqual([args]);
    }
  });

  it("picks POST for a new server and PUT for an existing one", async () => {
    await A.saveMcpServer("", { n: 1 });
    await A.saveMcpServer("a/b", { n: 2 });
    await A.saveTenantMcpServer("", { n: 3 });
    await A.saveTenantMcpServer("a/b", { n: 4 });
    expect(apiJSON.mock.calls).toEqual([
      ["api/mcp-servers", "POST", { n: 1 }],
      ["api/mcp-servers/a%2Fb", "PUT", { n: 2 }],
      ["api/admin/mcp-servers", "POST", { n: 3 }],
      ["api/admin/mcp-servers/a%2Fb", "PUT", { n: 4 }],
    ]);
  });

  it("other writes", async () => {
    await A.testMcpServer({ t: 1 });
    await A.deleteMcpServer("a/b");
    await A.refreshTenantMcp();
    await A.putMcpSecrets("a/b", { H: "v" });
    await A.deleteTenantMcpServer("a/b", "t&1");
    await A.proposeEgress("h.example", "why");
    expect(apiJSON.mock.calls).toEqual([
      ["api/mcp-servers/test", "POST", { t: 1 }],
      ["api/mcp-servers/a%2Fb", "DELETE"],
      ["api/mcp-servers/tenant-refresh", "POST", {}],
      ["api/mcp-servers/a%2Fb/secrets", "PUT", { headers: { H: "v" } }],
      ["api/admin/mcp-servers/a%2Fb?tenant=t%261", "DELETE"],
      ["api/egress/propose", "POST", { entry: "h.example", reason: "why" }],
    ]);
  });

  it("hands a null body through untouched", async () => {
    api.mockResolvedValue(null);
    expect(await A.getMcpRegistry()).toBeNull();
  });
});

describe("mcp typed API layer: source pins", () => {
  const dir = fileURLToPath(new URL("./", import.meta.url));
  const read = (name: string) => readFileSync(dir + name, "utf8");

  it("no component in the area imports api() / apiJSON() from the client (mcpOAuthApi.ts and mcpApi.ts are the wrappers)", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && !/Api\.ts$/.test(f));
    expect(files.length).toBeGreaterThan(8);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*["'][^"']*core\/api\/client\.ts["']/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps every path literal in mcpApi.ts", () => {
    const src = read("mcpApi.ts");
    for (const p of [
      '"api/mcp-servers"',
      "api/mcp-servers/${encodeURIComponent(id)}",
      '"api/mcp-servers/test"',
      '"api/mcp-servers/tenant-refresh"',
      "/secrets`",
      '"api/admin/mcp-servers?tenant="',
      '"api/admin/mcp-servers/"',
      '"api/admin/mcp-servers"',
      '"api/egress/check?"',
      '"api/egress/propose"',
    ]) {
      expect(src).toContain(p);
    }
  });
});
