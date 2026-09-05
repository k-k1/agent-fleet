import { describe, expect, it } from "vitest";
import { applySkillToDraft, exactSkills, filterSkills, hasTriggerHead, originKind, pickerTokenAt, slashTokenAt } from "./skillPicker.ts";
import type { SessionSkill } from "../../core/api/client.ts";

const sk = (name: string, description = "", type: SessionSkill["type"] = "skill"): SessionSkill => ({
  name,
  description,
  source: "project",
  type,
  invoke: "/" + name + " ",
});

describe("slashTokenAt", () => {
  it("is alive only inside the single token following the leading trigger", () => {
    expect(slashTokenAt("/", 1)).toEqual({ token: "", start: 0, end: 1 });
    expect(slashTokenAt("/pro", 4)).toEqual({ token: "pro", start: 0, end: 4 });
    expect(slashTokenAt("/pro arg", 3)).toEqual({ token: "pro", start: 0, end: 4 });
  });
  it("returns null out of scope: not at the head, empty text, caret left of the trigger", () => {
    expect(slashTokenAt("", 0)).toBeNull();
    expect(slashTokenAt("hello /x", 8)).toBeNull();
    expect(slashTokenAt("/pro", 0)).toBeNull(); // caret is left of the trigger
  });
  it("gives a passive token with args=true while arguments are typed (the range stays the head token)", () => {
    expect(slashTokenAt("/pro arg", 6)).toEqual({ token: "pro", start: 0, end: 4, args: true });
    expect(slashTokenAt("/pro ", 5)).toEqual({ token: "pro", start: 0, end: 4, args: true });
    expect(slashTokenAt("/pro\narg", 6)).toEqual({ token: "pro", start: 0, end: 4, args: true }); // same after a newline
  });
  it("uses a kind-dependent trigger character (codex's $ mention)", () => {
    expect(slashTokenAt("$ima", 4, "$")).toEqual({ token: "ima", start: 0, end: 4 });
    expect(slashTokenAt("/ima", 4, "$")).toBeNull();
    expect(slashTokenAt("$ima", 4, "")).toBeNull(); // a kind with no trigger never opens
  });
  it("also opens on the full-width aliases a Japanese IME types (／ and ＄)", () => {
    expect(slashTokenAt("／pro", 4, "/")).toEqual({ token: "pro", start: 0, end: 4 });
    expect(slashTokenAt("＄ima", 4, "$")).toEqual({ token: "ima", start: 0, end: 4 });
    expect(hasTriggerHead("／pro", "/")).toBe(true);
    expect(hasTriggerHead("pro", "/")).toBe(false);
  });
});

describe("pickerTokenAt", () => {
  it("allowBare=false behaves exactly like slashTokenAt (trigger required)", () => {
    expect(pickerTokenAt("pro", 3, "/")).toBeNull();
    expect(pickerTokenAt("/pro", 4, "/")).toEqual({ token: "pro", start: 0, end: 4 });
  });
  it("allowBare=true (button-initiated) also picks up a leading token with no trigger", () => {
    expect(pickerTokenAt("pro", 3, "/", true)).toEqual({ token: "pro", start: 0, end: 3, bare: true });
    expect(pickerTokenAt("", 0, "/", true)).toEqual({ token: "", start: 0, end: 0, bare: true });
    // Filtering also works for a kind with no trigger (kiro/copilot/agy - the button is the only way in)
    expect(pickerTokenAt("dep", 3, "", true)).toEqual({ token: "dep", start: 0, end: 3, bare: true });
  });
  it("returns null past the first bare word, i.e. while arguments are typed, leaving everything listed", () => {
    expect(pickerTokenAt("メモ 書き", 4, "/", true)).toBeNull();
    expect(pickerTokenAt("pro arg", 7, "/", true)).toBeNull();
  });
  it("defers to slashTokenAt for a draft with a trigger (an args token at the argument position)", () => {
    expect(pickerTokenAt("/pro arg", 6, "/", true)).toEqual({ token: "pro", start: 0, end: 4, args: true });
    expect(pickerTokenAt("/pro arg", 6, "/")).toEqual({ token: "pro", start: 0, end: 4, args: true });
  });
});

describe("originKind", () => {
  it("maps the origin convention dir to a kind (.agents is shared = null)", () => {
    expect(originKind(".claude")).toBe("claude");
    expect(originKind(".codex")).toBe("codex");
    expect(originKind(".agents")).toBeNull();
    expect(originKind(undefined)).toBeNull();
  });
});

describe("filterSkills", () => {
  const skills = [sk("handoff", "引き継ぎ"), sk("proofread", "原稿の整備"), sk("review", "proof をレビュー")];
  it("returns everything unchanged for an empty query", () => {
    expect(filterSkills(skills, "")).toEqual(skills);
  });
  it("orders by prefix match > name substring > description match", () => {
    const got = filterSkills(skills, "proof").map((s) => s.name);
    expect(got).toEqual(["proofread", "review"]);
    expect(filterSkills(skills, "OOF").map((s) => s.name)).toEqual(["proofread", "review"]);
  });
  it("drops entries that match nowhere", () => {
    expect(filterSkills(skills, "zzz")).toEqual([]);
  });
});

describe("exactSkills", () => {
  const foreign: SessionSkill = { name: "shared", source: "project", type: "skill", path: ".agents/shared/SKILL.md" };
  const skills = [sk("handoff", "引き継ぎ"), sk("hand", "別物"), foreign];
  it("keeps only the exactly named native item (the one whose argument hint is shown)", () => {
    expect(exactSkills(skills, "handoff").map((s) => s.name)).toEqual(["handoff"]);
    expect(exactSkills(skills, "HANDOFF").map((s) => s.name)).toEqual(["handoff"]);
  });
  it("lists nothing for a partial match, an empty query, or a foreign item with no invoke", () => {
    expect(exactSkills(skills, "hando")).toEqual([]);
    expect(exactSkills(skills, "")).toEqual([]);
    expect(exactSkills(skills, "shared")).toEqual([]);
  });
});

describe("applySkillToDraft", () => {
  it("replaces the token being typed and keeps the existing arguments", () => {
    expect(applySkillToDraft("/pro", 4, "/proofread ")).toEqual({ next: "/proofread ", caret: 11 });
    expect(applySkillToDraft("/pro Ph1 01", 4, "/proofread ")).toEqual({
      next: "/proofread Ph1 01",
      caret: 11,
    });
  });
  it("button-initiated (outside a token) inserts at the head and keeps the draft as arguments", () => {
    expect(applySkillToDraft("", 0, "/handoff ")).toEqual({ next: "/handoff ", caret: 9 });
    expect(applySkillToDraft("メモ書き", 2, "/handoff ")).toEqual({ next: "/handoff メモ書き", caret: 9 });
    // A draft that already starts with the trigger is not stacked: only the command is replaced,
    // the arguments stay
    expect(applySkillToDraft("/old args", 9, "/handoff ")).toEqual({ next: "/handoff args", caret: 9 });
  });
  it("allowBare (filtering after opening from the button) replaces the leading token used as the query", () => {
    // "hand" was typed to narrow the list, so it must not be left behind as an argument
    expect(applySkillToDraft("hand", 4, "/handoff ", "/", true)).toEqual({ next: "/handoff ", caret: 9 });
    // The second word onwards is an argument (it never filtered anything), so keep it whole
    expect(applySkillToDraft("メモ 書き", 5, "/handoff ", "/", true)).toEqual({
      next: "/handoff メモ 書き",
      caret: 9,
    });
  });
  it("builds the same way for codex's $ mention", () => {
    expect(applySkillToDraft("$ima", 4, "$imagegen ", "$")).toEqual({ next: "$imagegen ", caret: 10 });
    expect(applySkillToDraft("ロゴを作って", 3, "$imagegen ", "$")).toEqual({
      next: "$imagegen ロゴを作って",
      caret: 10,
    });
  });
  it("replaces a full-width trigger with the correct half-width invocation", () => {
    expect(applySkillToDraft("／pro", 4, "/proofread ")).toEqual({ next: "/proofread ", caret: 11 });
    expect(applySkillToDraft("＄ima", 4, "$imagegen ", "$")).toEqual({ next: "$imagegen ", caret: 10 });
  });
});
