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
    expect(rule(ui, ":root")).toContain("--fs-11: 11px");
    expect(rule(ui, ":root")).toContain("--fs-12: 12px");
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
    expect(r).toContain("font: inherit");
    expect(r).toContain("color: var(--fg)");
    expect(r).toContain("cursor: pointer");
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

describe(".usage-chip on .ui-chip", () => {
  const usage = strip(readFileSync(path.resolve(__dirname, "../features/usage/usage.css"), "utf8"));
  it("keeps the look the standalone .usage-chip had", () => {
    const r = rule(usage, ".usage-chips .usage-chip");
    expect(r).toContain("border-color: var(--btn-border)");
    expect(r).toContain("border-radius: 999px");
    expect(r).toContain("background: var(--sel-bg)");
    expect(r).toContain("font-size: var(--fs-12)");
    expect(r).toContain("padding: 2px 8px");
    expect(r).toContain("gap: 5px");
    expect(rule(usage, ".usage-chips .usage-chip:hover")).toBe("border-color: var(--btn-border);");
  });
  it("the call site carries ui-chip and no px font-size literal of 10..13 is left", () => {
    const src = readFileSync(path.resolve(__dirname, "../features/usage/UsageView.tsx"), "utf8");
    expect(src).toContain('className="ui-chip usage-chip"');
    expect(usage).not.toMatch(/font-size:\s*1[0-3]px/);
  });
});

describe("badges and chips moved onto the primitives (#4785)", () => {
  const read = (rel: string) => readFileSync(path.resolve(__dirname, rel), "utf8");
  const repos = strip(read("../features/repos/repos.css"));
  const files = strip(read("../features/files/files.css"));
  const sched = strip(read("../features/schedules/schedules.css"));
  it("repo-chip keeps only layout and sets the tone through --badge", () => {
    expect(rule(repos, ".repo-chip")).toBe("display: inline-flex; align-items: center; gap: 3px;");
    expect(rule(repos, ".repo-chip.dirty")).toContain("--badge: var(--warn)");
    expect(rule(repos, ".repo-chip.integration.repo-chip--contained")).toContain("--badge: var(--ok)");
    expect(rule(repos, ".repo-chip.ab.ff")).toContain("--badge: var(--ok)");
    expect(rule(repos, ".repo-chip.ab.diverged")).toContain("--badge: var(--warn)");
  });
  it("every repo-chip / chg-badge / sched-badge call site also carries ui-badge", () => {
    for (const [f, cls] of [
      ["../features/repos/RepoRow.tsx", "repo-chip"],
      ["../features/overview/SessionCard.tsx", "repo-chip"],
      ["../features/scm/SourceControlView.tsx", "repo-chip"],
      ["../features/mirror/FileChangeStrip.tsx", "chg-badge"],
      ["../features/project/FilesChanges.tsx", "chg-badge"],
      ["../features/schedules/SchedulesSection.tsx", "sched-badge"],
    ] as const) {
      const src = read(f);
      const all = src.match(new RegExp(`${cls}(?![-\\w])`, "g"))?.length ?? 0;
      const withPrimitive = src.match(new RegExp(`ui-badge[\\w -]* ?${cls}(?![-\\w])`, "g"))?.length ?? 0;
      expect(all, f).toBeGreaterThan(0);
      expect(withPrimitive, f).toBe(all);
    }
  });
  it("chg-badge tones and the sched badge keep colour out of their own rules", () => {
    expect(rule(files, ".chg-badge.st-del")).toBe("--badge: var(--del); --badge-mix: 16%;");
    expect(rule(sched, ".sched-badge")).not.toContain("color");
    expect(read("../features/schedules/SchedulesSection.tsx")).toContain("ui-badge ui-badge-warn sched-badge");
  });
  it("inbox chips ride .ui-chip", () => {
    expect(read("../features/inbox/InboxModal.tsx")).toContain('className="ui-chip inbox-chip"');
  });
});

describe(".ui-card primitive", () => {
  it("keeps the border, radius and default fill of the old inbox card", () => {
    const r = rule(ui, ".ui-card");
    expect(r).toContain("border: 1px solid var(--border)");
    expect(r).toContain("border-radius: 8px");
    expect(r).toContain("background: var(--card-bg, var(--panel))");
  });
  it("usage cards keep their panel2 fill through --card-bg", () => {
    const usage = strip(readFileSync(path.resolve(__dirname, "../features/usage/usage.css"), "utf8"));
    expect(rule(usage, ".usage-card")).toContain("--card-bg: var(--panel2)");
  });
  it("every inbox-card and usage-card call site also carries ui-card", () => {
    const sites: [string, string, number][] = [
      ["../features/inbox/InboxModal.tsx", "inbox-card", 2],
      ["../features/usage/UsageView.tsx", "usage-card", 5],
    ];
    for (const [f, cls, n] of sites) {
      const src = readFileSync(path.resolve(__dirname, f), "utf8");
      const all = src.match(new RegExp(`(?<![-\\w])${cls}(?![-\\w])`, "g"))?.length;
      const withPrimitive = src.match(new RegExp(`ui-card ${cls}(?![-\\w])`, "g"))?.length;
      expect(all).toBe(n);
      expect(withPrimitive).toBe(n);
    }
  });
});
