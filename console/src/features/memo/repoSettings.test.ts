// The repository settings card's reading and rows (#4881).
import { describe, expect, it } from "vitest";
import { approvedKeys, readRepoExport, readRepoSettings, repoDiffRows, repoKeyLabel, repoValueText } from "./repoSettings.ts";

const commit = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff";
const diff = (key: string, over: Record<string, unknown> = {}) => ({ key, field: key, line: 4, current: 1, file: 2, ...over });

describe("readRepoSettings", () => {
  it("reads an answer and drops what it cannot use", () => {
    const r = readRepoSettings({
      categoryId: "c1",
      commit,
      found: true,
      entry: true,
      approvalTag: "t",
      errors: null,
      warnings: [{ line: 3, key: "enabled", reason: "Console only" }, "junk"],
      diffs: [diff("batch", { raise: true }), { field: "x" }, null],
    })!;
    expect(r.found && r.entry).toBe(true);
    expect(r.errors).toEqual([]);
    expect(r.warnings).toEqual([{ line: 3, key: "enabled", reason: "Console only" }]);
    expect(r.diffs).toEqual([{ key: "batch", field: "batch", line: 4, current: 1, file: 2, raise: true }]);
  });

  it("never offers a difference beside an error", () => {
    const r = readRepoSettings({ categoryId: "c1", commit, errors: [{ line: 2, reason: "bad" }], diffs: [diff("batch")] })!;
    expect(r.errors).toHaveLength(1);
    expect(r.diffs).toEqual([]);
  });

  it("answers null for an error body or a foreign shape", () => {
    expect(readRepoSettings({ error: { code: "not_found" } })).toBeNull();
    expect(readRepoSettings({ categoryId: "c1" })).toBeNull();
    expect(readRepoSettings(null)).toBeNull();
  });
});

describe("readRepoExport", () => {
  it("needs the YAML", () => {
    expect(readRepoExport({ path: "p", category: "c", yaml: "version: 1\n", omitted: null })).toEqual({
      path: "p",
      category: "c",
      yaml: "version: 1\n",
      omitted: [],
    });
    expect(readRepoExport({ yaml: "" })).toBeNull();
    expect(readRepoExport({ error: {} })).toBeNull();
  });
});

describe("repoDiffRows", () => {
  it("puts the settings that decide when it runs in one row, at the place of the first", () => {
    const rows = repoDiffRows([
      { ...diff("spend_cap_usd"), raise: false },
      { ...diff("open_cron"), raise: false },
      { ...diff("max_active"), raise: true },
      { ...diff("open_after"), raise: false },
    ]);
    expect(rows.map((r) => r.id)).toEqual(["spend_cap_usd", "firing", "max_active"]);
    expect(rows[1].keys).toEqual(["open_cron", "open_after"]);
    expect(rows[2].raise).toBe(true);
    expect(approvedKeys(rows, new Set(["firing", "spend_cap_usd"]))).toEqual(["spend_cap_usd", "open_cron", "open_after"]);
    expect(approvedKeys(rows, new Set())).toEqual([]);
  });
});

describe("words", () => {
  it("labels known keys and reads values", () => {
    expect(repoKeyLabel("spend_cap_usd")).toBe("memo.q_budget");
    expect(repoKeyLabel("future_key")).toBeNull();
    expect(repoValueText("spend_cap_usd", 0)).toEqual({ word: "memo.repo_no_cap" });
    expect(repoValueText("max_active", 0)).toEqual({ text: "0" });
    expect(repoValueText("model", "")).toEqual({ word: "memo.repo_default" });
    expect(repoValueText("worktree", true)).toEqual({ word: "memo.repo_on" });
    expect(repoValueText("model", "m")).toEqual({ text: "m" });
  });
});
