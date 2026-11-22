import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pin for the agents area's typed API layer (#4783 part 5): a component that goes back to a
// raw `api()` / `apiJSON()` call gets an `any` reply again, and a wrapper whose path stops being a
// literal drops out of control-plane/console_routes_test.go, which reads the paths as text.
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("agents typed API layer", () => {
  it("nothing in the area outside agentsApi.ts imports api() / apiJSON() from the client", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && !/Api\.ts$/.test(f));
    expect(files.length).toBeGreaterThan(3);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*"[^"]*core\/api\/client\.ts"/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps the wrapper paths literal in agentsApi.ts", () => {
    const src = read("agentsApi.ts");
    for (const p of ["api/connections/${kind}/install"]) {
      expect(src).toContain(p);
    }
  });
});
