import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pins for the tenant area's typed API layer (#4783 part 3). Two seams break silently:
// a component that goes back to a raw `api()` call (the reply is `any` again), and a wrapper
// whose path stops being a literal (control-plane/console_routes_test.go reads them as text).
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("tenant typed API layer", () => {
  it("no tenant component calls api() / apiJSON() from the client directly", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && f !== "tenantApi.ts");
    expect(files.length).toBeGreaterThan(20);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*"[^"]*core\/api\/client\.ts"/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps every tenant path a literal in tenantApi.ts", () => {
    const src = read("tenantApi.ts");
    for (const p of [
      "api/admin/tenants/${T(slug)}/members",
      "api/admin/tenants/${T(slug)}/watch-sources",
      "api/admin/tenants/${T(slug)}/mcp-oauth",
      "api/admin/tenants/${T(slug)}/limits",
      "api/admin/tenants/${T(slug)}/idp/${encodeURIComponent(id)}/status",
      "api/admin/tenants/${T(slug)}/git-oauth/${encodeURIComponent(provider)}",
      "api/admin/tenants/${T(slug)}/members/${T(key)}/home-backups",
      "api/admin/providers",
      "api/admin/stop-workspace",
      "api/admin/user-limits",
      "api/admin/ec2-pool/reserve-outdated",
      'const SLOT = "api/admin/ec2-pool/slots/"',
    ]) {
      expect(src).toContain(p);
    }
  });
});
