// Drafting the report comment (docs/log/80 §80.10). What gets posted is exactly the string built
// here, so these tests pin down what it must NOT write rather than what it writes.
import { describe, expect, it } from "vitest";
import { composeReportDraft, REPORT_FILE_CAP, reportFilePaths, reportTarget } from "./report.ts";
import type { WorkItem, WorkItemSessionRef } from "./read.ts";

const item: WorkItem = {
  id: "1", queryId: "q1", provider: "github", kind: "issue",
  key: "acme/web#45", title: "ログイン後に一覧が空になる", state: "open",
  url: "https://github.com/acme/web/issues/45", assignee: "taro", labels: [],
  repo: "acme/web", updatedAt: "2026-08-26T00:00:00Z",
};
const session: WorkItemSessionRef = {
  id: "l1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9",
  repo: "web", branch: "feature/issue-45", createdAt: "",
};
const file = (repo: string, rel: string) => ({ path: `repos/${repo}/${rel}`, repo, rel, verb: "edit", count: 1, lastIdx: 0 });

describe("reportFilePaths", () => {
  it("emits repo-relative paths (a working-copy folder name means nothing to the ticket reader)", () => {
    const rows = [file("web", "b.ts"), file("web", "a.ts"), file("web", "a.ts")];
    expect(reportFilePaths(rows)).toEqual(["a.ts", "b.ts"]);
  });

  it("never leaks the worktree slug", () => {
    const rows = [file("webshop@checkout-validation", "src/checkout/validate.ts")];
    expect(reportFilePaths(rows)).toEqual(["src/checkout/validate.ts"]);
  });

  it("prefixes the base repo name when two working copies are involved, so paths cannot collide", () => {
    const rows = [file("webshop@wip-a", "src/index.ts"), file("payments-api", "src/index.ts")];
    expect(reportFilePaths(rows)).toEqual(["payments-api/src/index.ts", "webshop/src/index.ts"]);
  });

  it("falls back to path for a file outside a working copy (no repo/rel)", () => {
    expect(reportFilePaths([{ path: "notes.md", verb: "edit", count: 1, lastIdx: 0 }])).toEqual(["notes.md"]);
  });
});

describe("composeReportDraft", () => {
  it("states the branch and the changed files as facts", () => {
    const body = composeReportDraft({ item, session, files: [file("web", "a.ts"), file("web", "b.ts")] });
    expect(body).toContain("acme/web#45");
    expect(body).toContain("feature/issue-45");
    expect(body).toContain("- a.ts");
    expect(body).toContain("- b.ts");
  });

  it("generates no summary: with an empty note the body starts with facts alone", () => {
    const body = composeReportDraft({ item, session, files: [] });
    // The title is a string written by a third party and must not be carried into the body.
    expect(body).not.toContain(item.title);
    expect(body.split("\n")[0]).toContain("acme/web#45");
  });

  it("puts the note first and the facts below it", () => {
    const body = composeReportDraft({ item, session, files: [], note: "調査だけ済ませました" });
    expect(body.startsWith("調査だけ済ませました")).toBe(true);
    expect(body).toContain("acme/web#45");
  });

  it("truncates a long file list and says how many are left (a 100-line comment is not a report)", () => {
    const files = Array.from({ length: REPORT_FILE_CAP + 5 }, (_, i) => file("web", `f${String(i).padStart(2, "0")}.ts`));
    const body = composeReportDraft({ item, session, files });
    expect(body.split("\n").filter((l) => l.startsWith("- ")).length).toBe(REPORT_FILE_CAP);
    expect(body).toContain("5");
  });

  it("is deterministic: same input, same string, with no clock or randomness", () => {
    const a = composeReportDraft({ item, session, files: [file("web", "a.ts")] });
    const b = composeReportDraft({ item, session, files: [file("web", "a.ts")] });
    expect(a).toBe(b);
  });

  it("omits the whole line when the branch is unknown, rather than emitting an empty label", () => {
    const body = composeReportDraft({ item, session: { ...session, branch: "" }, files: [] });
    expect(body).not.toMatch(/^.*:\s*$/m);
  });
});

describe("reportTarget", () => {
  it("names the destination by key and title", () => {
    expect(reportTarget(item)).toBe("acme/web#45 — ログイン後に一覧が空になる");
  });
});
