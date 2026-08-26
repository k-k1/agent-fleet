// 報告コメントの下書き（docs/80 §80.10）。投稿されるのはここが作った文字列そのものなので、
// 「何を書くか」より「何を書かないか」を固定する。
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
  it("★ リポジトリ相対で出す（作業コピーのフォルダ名は課題の読み手に無意味）", () => {
    const rows = [file("web", "b.ts"), file("web", "a.ts"), file("web", "a.ts")];
    expect(reportFilePaths(rows)).toEqual(["a.ts", "b.ts"]);
  });

  it("★ worktree の slug を漏らさない（実描画で見つけた不具合）", () => {
    const rows = [file("webshop@checkout-validation", "src/checkout/validate.ts")];
    expect(reportFilePaths(rows)).toEqual(["src/checkout/validate.ts"]);
  });

  it("作業コピーが 2 つ以上なら base 名を前置する（衝突して 1 本消えるのを防ぐ）", () => {
    const rows = [file("webshop@wip-a", "src/index.ts"), file("payments-api", "src/index.ts")];
    expect(reportFilePaths(rows)).toEqual(["payments-api/src/index.ts", "webshop/src/index.ts"]);
  });

  it("作業コピーの外（repo/rel が無い）は path で載せる", () => {
    expect(reportFilePaths([{ path: "notes.md", verb: "edit", count: 1, lastIdx: 0 }])).toEqual(["notes.md"]);
  });
});

describe("composeReportDraft", () => {
  it("ブランチと変更ファイルを事実として書く", () => {
    const body = composeReportDraft({ item, session, files: [file("web", "a.ts"), file("web", "b.ts")] });
    expect(body).toContain("acme/web#45");
    expect(body).toContain("feature/issue-45");
    expect(body).toContain("- a.ts");
    expect(body).toContain("- b.ts");
  });

  it("★ 要約は生成しない —— ひとことが空なら本文は事実だけで始まる", () => {
    const body = composeReportDraft({ item, session, files: [] });
    // タイトル（＝第三者が書いた文字列）を本文へ持ち込まない。
    expect(body).not.toContain(item.title);
    expect(body.split("\n")[0]).toContain("acme/web#45");
  });

  it("ひとことは先頭に置き、事実はその下に続く", () => {
    const body = composeReportDraft({ item, session, files: [], note: "調査だけ済ませました" });
    expect(body.startsWith("調査だけ済ませました")).toBe(true);
    expect(body).toContain("acme/web#45");
  });

  it("ファイルが多いときは打ち切って残数を言う（100 行のコメントは報告ではない）", () => {
    const files = Array.from({ length: REPORT_FILE_CAP + 5 }, (_, i) => file("web", `f${String(i).padStart(2, "0")}.ts`));
    const body = composeReportDraft({ item, session, files });
    expect(body.split("\n").filter((l) => l.startsWith("- ")).length).toBe(REPORT_FILE_CAP);
    expect(body).toContain("5");
  });

  it("同じ入力なら同じ文字列（時刻も乱数も混ぜない）", () => {
    const a = composeReportDraft({ item, session, files: [file("web", "a.ts")] });
    const b = composeReportDraft({ item, session, files: [file("web", "a.ts")] });
    expect(a).toBe(b);
  });

  it("ブランチ不明なら行ごと出さない（空の「ブランチ:」を出さない）", () => {
    const body = composeReportDraft({ item, session: { ...session, branch: "" }, files: [] });
    expect(body).not.toMatch(/^.*:\s*$/m);
  });
});

describe("reportTarget", () => {
  it("宛先はキーとタイトル", () => {
    expect(reportTarget(item)).toBe("acme/web#45 — ログイン後に一覧が空になる");
  });
});
