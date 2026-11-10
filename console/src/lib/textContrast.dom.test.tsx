// applyTheme() writes data-contrast for Strong / Strongest and nothing for Standard; the token
// override selectors in tokens.css reach the root and a nested region of the other theme.
import { describe, it, expect, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { applyTheme } from "./settings/appearance.ts";
import { DEFAULTS } from "./settings/prefs.ts";
const defaultSettings = () => ({ ...DEFAULTS });

const css = readFileSync(path.resolve(__dirname, "../styles/tokens.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
function selectors(level: string, theme: string): string[] {
  const m = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((x) =>
    x[1].includes(`:root[data-contrast="${level}"][data-theme="${theme}"]`));
  return m ? m[1].split(",").map((s) => s.trim()) : [];
}
const matches = (el: Element, sels: string[]) => sels.some((s) => el.matches(s));

describe("text contrast", () => {
  beforeEach(() => {
    delete document.documentElement.dataset.contrast;
    document.body.innerHTML = "";
  });

  it("standard writes no attribute; strong / strongest write theirs; switching back removes it", () => {
    const root = document.documentElement;
    applyTheme({ ...defaultSettings(), textContrast: "standard" });
    expect(root.hasAttribute("data-contrast")).toBe(false);
    applyTheme({ ...defaultSettings(), textContrast: "strong" });
    expect(root.dataset.contrast).toBe("strong");
    applyTheme({ ...defaultSettings(), textContrast: "strongest" });
    expect(root.dataset.contrast).toBe("strongest");
    applyTheme({ ...defaultSettings(), textContrast: "standard" });
    expect(root.hasAttribute("data-contrast")).toBe(false);
  });

  it("an unknown stored value falls back to standard", () => {
    applyTheme({ ...defaultSettings(), textContrast: "bogus" as never });
    expect(document.documentElement.hasAttribute("data-contrast")).toBe(false);
  });

  for (const level of ["strong", "strongest"]) {
    it(`${level}: override reaches the root and a nested region of the opposite theme`, () => {
      for (const [appTheme, region] of [["dark", "light"], ["light", "dark"]] as const) {
        applyTheme({ ...defaultSettings(), theme: appTheme, textContrast: level as "strong" });
        document.body.innerHTML = `<div id="r" data-theme="${region}"><span id="in"></span></div>`;
        expect(matches(document.documentElement, selectors(level, appTheme)), `root ${appTheme}`).toBe(true);
        expect(matches(document.getElementById("r")!, selectors(level, region)), `region ${region} in ${appTheme}`).toBe(true);
        // the region must not pick up the other theme's override
        expect(matches(document.getElementById("r")!, selectors(level, appTheme))).toBe(false);
      }
    });
  }
});
