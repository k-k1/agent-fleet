import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { hasCodeLiteral, importsRawClient } from "../../../test/sourcePins.ts";

// Source pin for the credentials area's typed API layer (#4783 part 5): a component that goes back to a
// raw `api()` / `apiJSON()` call gets an `any` reply again, and a wrapper whose path stops being a
// literal drops out of control-plane/console_routes_test.go, which reads the paths as text.
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("credentials typed API layer", () => {
  it("nothing in the area outside credentialsApi.ts imports api() / apiJSON() from the client", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && f !== "credentialsApi.ts");
    expect(files.length).toBeGreaterThan(8);
    const offenders = files.filter((f) =>
      importsRawClient(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps the wrapper paths literal in credentialsApi.ts", () => {
    const src = read("credentialsApi.ts");
    for (const p of [
      '"api/connections/claude/start"',
      '"api/connections/muse/poll"',
      '"api/connections/codex/device/start"',
      '"api/connections/opencode/oauth/cancel"',
      '"api/connections/lcpp/check"',
      '"api/connections/unusable/remove"',
      '"api/agents/kinds"',
    ]) {
      expect(hasCodeLiteral(src, p)).toBe(true);
    }
  });
});
