import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pins for the workspace area's typed API layer (#4783 part 4). Two seams break silently:
// a component that goes back to a raw `api()` call (the reply is `any` again), and a wrapper
// whose path stops being a literal (control-plane/console_routes_test.go reads them as text).
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("workspace typed API layer", () => {
  it("nothing in the area outside the *Api.ts wrappers imports api() / apiJSON() from the client", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && !/Api\.ts$/.test(f));
    expect(files.length).toBeGreaterThan(20);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*"[^"]*core\/api\/client\.ts"/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps every workspace path a literal in workspaceApi.ts", () => {
    const src = read("workspaceApi.ts");
    for (const p of [
      '"api/workspace/machine"',
      '"api/cleanup/usage"',
      '"api/cleanup/leftovers"',
      '"api/cleanup/tool-caches"',
      '"api/cleanup/af-work"',
      '"api/cleanup/home/measure"',
      '"api/cleanup/home"',
      '"api/workspace/sizing"',
      '"api/workspace/boost"',
      '"api/env/databases"',
      "api/env/databases/${engine}/${action}",
      '"api/sessions"',
      '"api/internal-git/repos"',
      "api/internal-git/repos/${encodeURIComponent(repo)}/branches",
      "api/internal-git/repos/${encodeURIComponent(oldName)}/rename",
      "api/internal-git/repos/${encodeURIComponent(name)}`",
      "/tree?ref=${encodeURIComponent(ref)}",
      "/blob?ref=${encodeURIComponent(ref)}",
      '"api/update/apply"',
      "api/env/${endpoint}?major=",
      '"api/env/jdk-install"',
      '"api/env/node-install"',
      '"api/env/toolchain-installs"',
      "api/env/${kind}/${encodeURIComponent(major)}",
      '"api/gcp-login/profiles"',
      '"api/gcp/profiles"',
    ]) {
      expect(src).toContain(p);
    }
  });
});
