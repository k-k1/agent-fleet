import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pins for the admin area's typed API layer (#4783 part 2). Two seams break silently:
// a component that goes back to a raw `api()` call (the reply is `any` again), and a wrapper
// whose path stops being a string literal (control-plane/console_routes_test.go reads them as
// text, so a path built by a helper is never checked against a route).
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("admin typed API layer", () => {
  it("no admin component calls api() / apiJSON() from the client directly", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && f !== "adminApi.ts");
    expect(files.length).toBeGreaterThan(20);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*"[^"]*core\/api\/client\.ts"/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps every admin path a literal in adminApi.ts", () => {
    const src = read("adminApi.ts");
    for (const p of [
      "api/admin/brand",
      "api/admin/tts/dict",
      "api/admin/dev-idp",
      "api/admin/mcp-oauth/trusted-hosts",
      "api/admin/route-policy/tenant-counts",
      "api/admin/egress/allowlist/",
      "api/admin/engines/hf-token",
      "api/admin/engines/${encodeURIComponent(key)}/replace-box",
      "api/admin/engines/${encodeURIComponent(key)}/ingest/resolve",
      "api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(modelId)}/complete",
      "api/admin/provided-routes/deployment",
    ]) {
      expect(src).toContain(p);
    }
  });

  it("forwards a DELETE without inventing a body argument", () => {
    expect(read("adminApi.ts")).toContain("rawApiJSON(...args)");
  });
});
