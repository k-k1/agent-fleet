import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Source pin for the chat area's typed API layer (#4783 part 5): a component that goes back to a
// raw `api()` / `apiJSON()` call gets an `any` reply again, and a wrapper whose path stops being a
// literal drops out of control-plane/console_routes_test.go, which reads the paths as text.
const dir = fileURLToPath(new URL("./", import.meta.url));
const read = (name: string) => readFileSync(dir + name, "utf8");

describe("chat typed API layer", () => {
  it("nothing in the area outside chatApi.ts imports api() / apiJSON() from the client", () => {
    const files = readdirSync(dir).filter((f) => /\.tsx?$/.test(f) && !/\.test\./.test(f) && !/Api\.ts$/.test(f));
    expect(files.length).toBeGreaterThan(2);
    const offenders = files.filter((f) =>
      /import\s*\{[^}]*\b(api|apiJSON)\b[^}]*\}\s*from\s*["'][^"']*core\/api\/client\.ts["']/.test(read(f)),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps the wrapper paths literal in chatApi.ts", () => {
    const src = read("chatApi.ts");
    for (const p of [
      '"api/connections/discord/inspect"',
      '"api/connections/discord/guilds"',
      '"api/connections/slack/inspect"',
      '"api/connections/slack/channels"',
      '"api/connections/slack"',
      "api/connections/${kind}`",
    ]) {
      expect(src).toContain(p);
    }
  });
});
