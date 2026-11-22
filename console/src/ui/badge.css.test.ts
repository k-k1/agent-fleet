import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { safetyBadgeClass } from "../features/sessions/cleanupGroups.ts";

const strip = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const ui = strip(readFileSync(path.resolve(__dirname, "ui.css"), "utf8"));
const sessions = strip(readFileSync(path.resolve(__dirname, "../features/sessions/sessions.css"), "utf8"));
const rule = (css: string, sel: string): string => {
  const m = css.match(new RegExp("(?:^|\\})\\s*" + sel.replace(/[.]/g, "\\.") + "\\s*\\{([^}]*)\\}"));
  if (!m) throw new Error("no rule " + sel);
  return m[1]!.replace(/\s+/g, " ").trim();
};

// The cleanup badges moved onto .ui-badge with no visual change: pin the values they had.
describe(".ui-badge primitive", () => {
  it("keeps the size, padding, radius and tint of the old clean badges", () => {
    const r = rule(ui, ".ui-badge");
    expect(r).toContain("font-size: var(--fs-10)");
    expect(r).toContain("padding: 1px 6px");
    expect(r).toContain("border-radius: 9px");
    expect(r).toContain("white-space: nowrap");
    expect(r).toContain("color: var(--badge)");
    expect(r).toContain("background: color-mix(in srgb, var(--badge) var(--badge-mix), transparent)");
    expect(r).toContain("--badge: var(--muted)");
    expect(r).toContain("--badge-mix: 14%");
  });
  it("tones tint 16% with the colours the old rules used", () => {
    expect(rule(ui, ".ui-badge-ok")).toBe("--badge: var(--ok); --badge-mix: 16%;");
    expect(rule(ui, ".ui-badge-warn")).toBe("--badge: var(--warn); --badge-mix: 16%;");
    expect(rule(ui, ".ui-badge-accent")).toBe("--badge: var(--accent, var(--ok)); --badge-mix: 16%;");
  });
  it("font-size tokens keep their literal values", () => {
    expect(rule(ui, ":root")).toContain("--fs-10: 10px");
    expect(rule(ui, ":root")).toContain("--fs-13: 13px");
  });
  it("the reason badge keeps flex:none and the cleanup call sites use the primitive", () => {
    expect(rule(sessions, ".clean-reason-badge")).toBe("flex: none;");
    for (const f of ["CleanupModal.tsx", "WorktreeOutputSection.tsx"]) {
      const src = readFileSync(path.resolve(__dirname, "../features/sessions", f), "utf8");
      expect(src).toContain("safetyBadgeClass(");
      expect(src).not.toContain("clean-badge");
    }
    const modal = readFileSync(path.resolve(__dirname, "../features/sessions/CleanupModal.tsx"), "utf8");
    expect(modal.match(/ui-badge clean-reason-badge/g)?.length).toBe(2);
    expect(safetyBadgeClass("keep")).toBe("ui-badge");
  });
});

describe(".ui-chip primitive", () => {
  it("keeps the size, padding, radius and colours of the old .igen-chip", () => {
    const r = rule(ui, ".ui-chip");
    expect(r).toContain("font-size: var(--fs-11)");
    expect(r).toContain("padding: 1px 7px");
    expect(r).toContain("border-radius: 10px");
    expect(r).toContain("border: 1px solid var(--border)");
    expect(r).toContain("background: var(--chip-bg, transparent)");
    expect(rule(ui, ".ui-chip:hover")).toBe("border-color: var(--accent, var(--fg));");
  });
  it("every igen-chip call site also carries ui-chip", () => {
    for (const f of ["GenerateForm.tsx", "StudioAgent.tsx"]) {
      const src = readFileSync(path.resolve(__dirname, "../features/imagegen/parts", f), "utf8");
      expect(src.match(/igen-chip(?![-\w])/g)?.length).toBe(src.match(/ui-chip igen-chip(?![-\w])/g)?.length);
    }
  });
});
