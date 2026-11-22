import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pin for the personal area's typed API layer (#4783 part 5): a component that goes back to a
// raw `api()` / `apiJSON()` call gets an `any` reply again, and a wrapper whose path stops being a
// literal drops out of control-plane/console_routes_test.go, which reads the paths as text.
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("personal typed API layer", () => {
  it("nothing in the area outside personalApi.ts imports api() / apiJSON() from the client", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && !/Api\.ts$/.test(f));
    expect(files.length).toBeGreaterThan(8);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*"[^"]*core\/api\/client\.ts"/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps the wrapper paths literal in personalApi.ts", () => {
    const src = read("personalApi.ts");
    for (const p of [
      '"api/me/login-methods"',
      '"api/user-notes"',
      '"api/skills/catalog"',
      '"api/ai-assist/resolution"',
    ]) {
      expect(src).toContain(p);
    }
  });
});
