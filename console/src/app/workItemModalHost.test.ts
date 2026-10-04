// WorkItemModalHost must be mounted exactly once in each of App's two shells (#1659): none, and a
// rail row or a ticket link updates a store nobody renders; two, and the detail modal stacks on
// itself with a doubled focus trap and Esc handler. Checked on the source because rendering App
// needs most of the Console's backends.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const src = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const count = (s: string) => (s.match(/<WorkItemModalHost \/>/g) || []).length;

describe("App mounts WorkItemModalHost", () => {
  it("once in the pop-out shell and once in the full one", () => {
    const at = src.indexOf('if (popout === "popout") {');
    expect(at).toBeGreaterThan(0);
    const popoutEnd = src.indexOf("\n  }\n", at);
    expect(count(src.slice(at, popoutEnd))).toBe(1);
    expect(count(src.slice(popoutEnd))).toBe(1);
  });
});
