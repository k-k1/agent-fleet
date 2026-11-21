// The activity-bar badge colours follow the top bar's bell (--err), not --accent, which is also
// the active view's indicator. Waiting keeps --warn.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./app.css", import.meta.url), "utf8");
const rule = (sel: string) => {
  const m = css.match(new RegExp(`${sel.replace(/\./g, "\\.")}\\s*\\{([^}]*)\\}`));
  return m ? m[1] : "";
};

describe("activity-bar badge tones", () => {
  it("paints news and unread like the bell, failed unchanged, waiting amber", () => {
    expect(rule(".actbar-badge.tone-news")).toContain("var(--err)");
    expect(rule(".actbar-badge.tone-unread,\n.actbar-badge.tone-failed")).toContain("var(--err)");
    expect(rule(".actbar-badge.tone-waiting")).toContain("var(--warn)");
    expect(rule(".actbar-badge")).toContain("color: var(--on-err)");
    expect(css).not.toMatch(/\.actbar-badge[^{]*\{[^}]*var\(--accent\)/);
  });
});
