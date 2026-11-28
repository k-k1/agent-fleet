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
    expect(rule(".wi-row--done .wi-title")).toMatch(/opacity:\s*0\.55;/);
    // Key, time and meta keep their colour on done rows, as Jira rows did before.
    expect(css).not.toMatch(/\.wi-row--done[^{]*\.wi-(key|meta|when|comments)/);
    expect(css).not.toMatch(/\.wi-row--done\s*\{/);
    expect(css).not.toMatch(/\.wi-row--done:not\(/);
  });

  it("colours the rail's Jira state cue with the detail pill's fixed tone", () => {
    const fixed = { blue: "#0c66e4", green: "#1f883d", grey: "#6e7781" };
    for (const [tone, hex] of Object.entries(fixed)) {
      expect(rule(`.wi-jira-state.tone-${tone}`)).toMatch(new RegExp(`color:\\s*${hex};`));
      expect(rule(`.wi-dstate.tone-${tone}`)).toMatch(new RegExp(`background:\\s*${hex};`));
    }
    const base = rule(".wi-jira-state");
    expect(base).not.toMatch(/var\(--/);
    // Light backing and ring keep the fixed tones legible on a dark pane (Slate, hover included).
    expect(base).toMatch(/background-color:\s*#fff;/);
    expect(base).toMatch(/box-shadow:\s*0 0 0 1px #fff;/);
    expect(rule(".wi-jira-state--in_progress")).toMatch(/linear-gradient\(90deg, currentColor 50%, transparent 50%\), #fff;/);
  });
});
