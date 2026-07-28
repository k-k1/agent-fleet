import { describe, expect, it } from "vitest";
import { applySkillToDraft, filterSkills, slashTokenAt } from "./skillPicker.ts";
import type { SessionSkill } from "../../core/api/client.ts";

const sk = (name: string, description = "", type: SessionSkill["type"] = "skill"): SessionSkill => ({
  name,
  description,
  source: "project",
  type,
  invoke: "/" + name + " ",
});

describe("slashTokenAt", () => {
  it("先頭トリガの 1 トークン内でだけ生きる", () => {
    expect(slashTokenAt("/", 1)).toEqual({ token: "", start: 0, end: 1 });
    expect(slashTokenAt("/pro", 4)).toEqual({ token: "pro", start: 0, end: 4 });
    expect(slashTokenAt("/pro arg", 3)).toEqual({ token: "pro", start: 0, end: 4 });
  });
  it("対象外は null（非先頭・トークン外・空文字）", () => {
    expect(slashTokenAt("", 0)).toBeNull();
    expect(slashTokenAt("hello /x", 8)).toBeNull();
    expect(slashTokenAt("/pro arg", 6)).toBeNull(); // 引数入力中は閉じる
    expect(slashTokenAt("/pro", 0)).toBeNull(); // キャレットがトリガより左
    expect(slashTokenAt("/pro\narg", 6)).toBeNull(); // 改行後も引数扱い
  });
  it("トリガ文字は kind 依存（codex の $ メンション）", () => {
    expect(slashTokenAt("$ima", 4, "$")).toEqual({ token: "ima", start: 0, end: 4 });
    expect(slashTokenAt("/ima", 4, "$")).toBeNull();
    expect(slashTokenAt("$ima", 4, "")).toBeNull(); // トリガ無し kind は開かない
  });
});

describe("filterSkills", () => {
  const skills = [sk("handoff", "引き継ぎ"), sk("proofread", "原稿の整備"), sk("review", "proof をレビュー")];
  it("空クエリは全件そのまま", () => {
    expect(filterSkills(skills, "")).toEqual(skills);
  });
  it("前方一致 > 名前部分一致 > 説明一致の順", () => {
    const got = filterSkills(skills, "proof").map((s) => s.name);
    expect(got).toEqual(["proofread", "review"]);
    expect(filterSkills(skills, "OOF").map((s) => s.name)).toEqual(["proofread", "review"]);
  });
  it("どこにも当たらなければ落とす", () => {
    expect(filterSkills(skills, "zzz")).toEqual([]);
  });
});

describe("applySkillToDraft", () => {
  it("入力中のトークンを置換し、既存引数は残す", () => {
    expect(applySkillToDraft("/pro", 4, "/proofread ")).toEqual({ next: "/proofread ", caret: 11 });
    expect(applySkillToDraft("/pro Ph1 01", 4, "/proofread ")).toEqual({
      next: "/proofread Ph1 01",
      caret: 11,
    });
  });
  it("ボタン起点（トークン外）は下書きを引数として先頭に差し込む", () => {
    expect(applySkillToDraft("", 0, "/handoff ")).toEqual({ next: "/handoff ", caret: 9 });
    expect(applySkillToDraft("メモ書き", 2, "/handoff ")).toEqual({ next: "/handoff メモ書き", caret: 9 });
    // 既にトリガで始まる下書きは重ねない（置き換え）
    expect(applySkillToDraft("/old args", 9, "/handoff ")).toEqual({ next: "/handoff ", caret: 9 });
  });
  it("codex の $ メンションでも同じ組み立て", () => {
    expect(applySkillToDraft("$ima", 4, "$imagegen ", "$")).toEqual({ next: "$imagegen ", caret: 10 });
    expect(applySkillToDraft("ロゴを作って", 3, "$imagegen ", "$")).toEqual({
      next: "$imagegen ロゴを作って",
      caret: 10,
    });
  });
});
