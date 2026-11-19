import { afterEach, describe, expect, it } from "vitest";
import { applyTheme } from "./appearance.ts";
import { ON_ACCENT_DARK as D } from "./onAccent.ts";
import { DEFAULTS } from "./prefs.ts";

describe("applyTheme on-colour variables", () => {
  const root = document.documentElement;
  const run = (over: object) => applyTheme({ ...DEFAULTS, ...over } as never);

  afterEach(() => {
    root.removeAttribute("style");
    root.removeAttribute("data-theme");
  });

  it("sets the on-colour beside the fill and removes both on reset", () => {
    run({ topbarColor: "green", leftpaneColor: "warm" });
    expect(root.style.getPropertyValue("--topbar-accent")).toBe("#2fb872");
    expect(root.style.getPropertyValue("--on-topbar-accent")).toBe(D);
    expect(root.style.getPropertyValue("--on-lp-accent")).toBe(D);
    expect(root.style.getPropertyValue("--lp-accent")).toBe("#e0964a");
    expect(root.style.getPropertyValue("--topbar-accent-hover")).toContain("#ffffff");
    expect(root.style.getPropertyValue("--lp-accent-hover")).toContain("#ffffff");
    run({ topbarColor: "default", leftpaneColor: "default" });
    expect(root.style.getPropertyValue("--topbar-accent")).toBe("");
    expect(root.style.getPropertyValue("--on-topbar-accent")).toBe("");
    expect(root.style.getPropertyValue("--on-lp-accent")).toBe("");
    expect(root.style.getPropertyValue("--lp-accent")).toBe("");
    expect(root.style.getPropertyValue("--topbar-accent-hover")).toBe("");
    expect(root.style.getPropertyValue("--lp-accent-hover")).toBe("");
  });
});
