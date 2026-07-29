import { describe, expect, it } from "vitest";
import { applySkillToDraft, filterSkills, hasTriggerHead, originKind, pickerTokenAt, slashTokenAt } from "./skillPicker.ts";
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
  it("全角エイリアス（JP IME の ／・＄）でも開く", () => {
    expect(slashTokenAt("／pro", 4, "/")).toEqual({ token: "pro", start: 0, end: 4 });
    expect(slashTokenAt("＄ima", 4, "$")).toEqual({ token: "ima", start: 0, end: 4 });
    expect(hasTriggerHead("／pro", "/")).toBe(true);
    expect(hasTriggerHead("pro", "/")).toBe(false);
  });
});

describe("pickerTokenAt", () => {
  it("allowBare=false は slashTokenAt と同じ（トリガ必須）", () => {
    expect(pickerTokenAt("pro", 3, "/")).toBeNull();
    expect(pickerTokenAt("/pro", 4, "/")).toEqual({ token: "pro", start: 0, end: 4 });
  });
  it("allowBare=true（ボタン起点）はトリガ無しの先頭トークンも拾う", () => {
    expect(pickerTokenAt("pro", 3, "/", true)).toEqual({ token: "pro", start: 0, end: 3, bare: true });
    expect(pickerTokenAt("", 0, "/", true)).toEqual({ token: "", start: 0, end: 0, bare: true });
    // トリガ無し kind（kiro/copilot/agy — ボタンだけが入口）でも絞り込める
    expect(pickerTokenAt("dep", 3, "", true)).toEqual({ token: "dep", start: 0, end: 3, bare: true });
  });
  it("2 語目以降＝引数を書いている間は null（全件のまま）", () => {
    expect(pickerTokenAt("メモ 書き", 4, "/", true)).toBeNull();
    expect(pickerTokenAt("pro arg", 7, "/", true)).toBeNull();
  });
  it("トリガ付きの下書きの判断は slashTokenAt に従う（bare で拾い直さない）", () => {
    expect(pickerTokenAt("/pro arg", 6, "/", true)).toBeNull();
  });
});

describe("originKind", () => {
  it("出所規約 dir → kind（.agents は共有 = null）", () => {
    expect(originKind(".claude")).toBe("claude");
    expect(originKind(".codex")).toBe("codex");
    expect(originKind(".agents")).toBeNull();
    expect(originKind(undefined)).toBeNull();
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
  it("allowBare（ボタン起点で絞り込み中）は、クエリに使った先頭トークンを置換する", () => {
    // "hand" は候補を絞るために打った文字なので引数に残さない
    expect(applySkillToDraft("hand", 4, "/handoff ", "/", true)).toEqual({ next: "/handoff ", caret: 9 });
    // 2 語目以降は引数扱い（絞り込みに使っていない）ので丸ごと残す
    expect(applySkillToDraft("メモ 書き", 5, "/handoff ", "/", true)).toEqual({
      next: "/handoff メモ 書き",
      caret: 9,
    });
  });
  it("codex の $ メンションでも同じ組み立て", () => {
    expect(applySkillToDraft("$ima", 4, "$imagegen ", "$")).toEqual({ next: "$imagegen ", caret: 10 });
    expect(applySkillToDraft("ロゴを作って", 3, "$imagegen ", "$")).toEqual({
      next: "$imagegen ロゴを作って",
      caret: 10,
    });
  });
  it("全角トリガの入力は正しい半角起動形へ置換される", () => {
    expect(applySkillToDraft("／pro", 4, "/proofread ")).toEqual({ next: "/proofread ", caret: 11 });
    expect(applySkillToDraft("＄ima", 4, "$imagegen ", "$")).toEqual({ next: "$imagegen ", caret: 10 });
  });
});
