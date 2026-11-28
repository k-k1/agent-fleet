import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./workitems.css", import.meta.url), "utf8");

describe("workitems.css", () => {
  it("dims every done row as a whole, whatever the provider", () => {
    expect(css).toMatch(/\.wi-row--done\s*\{\s*opacity:/);
    expect(css).not.toMatch(/\.wi-row--done[^{]*\.wi-title/);
    expect(css).not.toMatch(/\.wi-row--done:not\(/);
  });

  it("colours the Jira state cue with fixed tones, not theme variables", () => {
    const cue = css.slice(css.indexOf(".wi-jira-state {"), css.indexOf(".wi-issue-progress {"));
    expect(cue).not.toMatch(/var\(--(accent|ok|muted)\)/);
    for (const tone of ["blue", "green", "grey"]) expect(cue).toContain(`.wi-jira-state.tone-${tone}`);
  });
});
