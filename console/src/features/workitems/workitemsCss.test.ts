import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./workitems.css", import.meta.url), "utf8");

/** The declaration block of the rule whose selector is exactly `selector`. */
function rule(selector: string): string {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const m = new RegExp(`(?:^|\\})\\s*${esc}\\s*\\{([^}]*)\\}`, "m").exec(css);
  expect(m, `rule ${selector}`).not.toBeNull();
  return m![1];
}

describe("workitems.css", () => {
  it("dims the text parts of a done row with one value, never the row itself", () => {
    expect(rule(".wi-row--done :is(.wi-key, .wi-title, .wi-meta, .wi-when, .wi-comments)")).toMatch(/opacity:\s*0\.55;/);
    expect(css).not.toMatch(/\.wi-row--done\s*\{/);
    expect(css).not.toMatch(/\.wi-row--done:not\(/);
  });

  it("colours the rail's Jira state cue with the detail pill's fixed tone", () => {
    const fixed = { blue: "#0c66e4", green: "#1f883d", grey: "#6e7781" };
    for (const [tone, hex] of Object.entries(fixed)) {
      expect(rule(`.wi-jira-state.tone-${tone}`)).toMatch(new RegExp(`color:\\s*${hex};`));
      expect(rule(`.wi-dstate.tone-${tone}`)).toMatch(new RegExp(`background:\\s*${hex};`));
    }
    expect(rule(".wi-jira-state")).not.toMatch(/var\(--/);
  });
});
