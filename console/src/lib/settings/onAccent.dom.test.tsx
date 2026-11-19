import { describe, expect, it } from "vitest";
import { applyTheme } from "./appearance.ts";
import { ON_ACCENT_DARK as D } from "./onAccent.ts";
import { DEFAULTS } from "./prefs.ts";

describe("applyTheme on-colour variables", () => {
  const root = document.documentElement;
  const run = (over: object) => applyTheme({ ...DEFAULTS, ...over } as never);

  it("sets the on-colour beside the fill and removes both on reset", () => {
    run({ topbarColor: "green", leftpaneColor: "warm" });
    expect(root.style.getPropertyValue("--topbar-accent")).toBe("#2fb872");
    expect(root.style.getPropertyValue("--on-topbar-accent")).toBe(D);
    expect(root.style.getPropertyValue("--on-lp-accent")).toBe(D);
    run({ topbarColor: "default", leftpaneColor: "default" });
    expect(root.style.getPropertyValue("--topbar-accent")).toBe("");
    expect(root.style.getPropertyValue("--on-topbar-accent")).toBe("");
    expect(root.style.getPropertyValue("--on-lp-accent")).toBe("");
  });
});
