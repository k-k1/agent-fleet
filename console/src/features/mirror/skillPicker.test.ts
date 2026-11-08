import { describe, expect, it } from "vitest";
import {
  applySkillToDraft,
  collapseCli,
  exactSkills,
  filterSkills,
  hasTriggerHead,
  originKind,
  pickerTokenAt,
  slashTokenAt,
  splitDoubleTrigger,
  splitWords,
  wordMatchCost,
} from "./skillPicker.ts";
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
    expect(originKind(".muse")).toBe("muse");
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
  it("puts the user's own entries ahead of CLI-bundled ones within a rank (docs/log/50 §9)", () => {
    const cli = (name: string, description = ""): SessionSkill => ({ ...sk(name, description), source: "cli" });
    const mixed = [cli("dataviz", "charts"), sk("handoff", "引き継ぎ"), cli("simplify", "cleanup"), sk("simulate", "sim run")];
    expect(filterSkills(mixed, "").map((s) => s.name)).toEqual(["handoff", "simulate", "dataviz", "simplify"]);
    // both prefix-match "sim": the project one first, then the bundled one; the description
    // match ranks below either
    expect(filterSkills(mixed, "sim").map((s) => s.name)).toEqual(["simulate", "simplify"]);
    // name-substring rank ("a"): own before bundled, then the description-only match last
    expect(filterSkills(mixed, "a").map((s) => s.name)).toEqual(["handoff", "simulate", "dataviz", "simplify"]);
  });
});

describe("splitWords", () => {
  it("splits kebab, snake, dotted, namespaced, camelCase and PascalCase names the same way", () => {
    for (const n of ["issue-to-pr", "issue_to_pr", "issue.to.pr", "issueToPr", "IssueToPr", "ISSUE_TO_PR"]) {
      expect(splitWords(n)).toEqual(["issue", "to", "pr"]);
    }
    expect(splitWords("plugin:skill-doctor")).toEqual(["plugin", "skill", "doctor"]);
    expect(splitWords("HTMLParser2Go")).toEqual(["html", "parser", "2", "go"]);
  });
  it("returns no words for an empty or separator-only string", () => {
    expect(splitWords("")).toEqual([]);
    expect(splitWords("-_-")).toEqual([]);
  });
  it("keeps non-Latin runs as words instead of dropping them", () => {
    expect(splitWords("レビュー-v2")).toEqual(["レビュー", "v", "2"]);
  });
});

describe("wordMatchCost", () => {
  it("matches word prefixes in order, whatever separator either side uses", () => {
    for (const q of ["iss-t", "iss_t", "issT", "issto", "itp", "i-t-p", "issue-to-pr", "isspr"]) {
      expect(wordMatchCost("issue-to-pr", q)).toBeGreaterThanOrEqual(0);
    }
    for (const n of ["issue_to_pr", "IssueToPr", "issueToPr"]) expect(wordMatchCost(n, "iss-t")).toBe(0);
  });
  it("counts skipped words as the cost", () => {
    expect(wordMatchCost("issue-to-pr", "iss-t")).toBe(0);
    expect(wordMatchCost("issue-to-pr", "to-pr")).toBe(1);
    expect(wordMatchCost("issue-to-pr", "isspr")).toBe(1);
  });
  it("rejects segments that do not start on a word, and plain subsequences", () => {
    expect(wordMatchCost("issue-rounds", "iss-t")).toBe(-1);
    expect(wordMatchCost("issue-to-pr", "ssue")).toBe(-1);
    expect(wordMatchCost("fewer-permission-prompts", "iss")).toBe(-1);
    expect(wordMatchCost("issue-to-pr", "pr-to")).toBe(-1); // order matters
    expect(wordMatchCost("issue-to-pr", "-")).toBe(-1);
    expect(wordMatchCost("---", "a")).toBe(-1); // a name made of separators only has no words
  });
  it("does not count unused trailing words, and matches digit words", () => {
    expect(wordMatchCost("issue-to-pr", "issue")).toBe(0);
    expect(wordMatchCost("deploy-v2", "d-2")).toBe(1);
  });
  it("treats a stray capital mid-word as a word break, so it misses (deliberate)", () => {
    // "IsST" splits into "is" + "st"; no word after "issue" starts with "st". Honouring capitals
    // is what makes "issT" and "IssueToPr" work, so this miss is the accepted trade-off.
    expect(wordMatchCost("issue-to-pr", "IsST")).toBe(-1);
  });
});

describe("filterSkills word matching", () => {
  const skills = [
    sk("issue-rounds", "Issue を子に配る"),
    sk("issue-to-pr", "要望を Issue に"),
    sk("fewer-permission-prompts"),
    sk("security-assessment", "issue の候補"),
    sk("code-review"),
    sk("prereview"),
  ];
  it("selects issue-to-pr first for iss-t (and its snake/camel spellings)", () => {
    for (const q of ["iss-t", "iss_t", "issT", "isst", "itp"]) {
      expect(filterSkills(skills, q)[0].name).toBe("issue-to-pr");
    }
    expect(filterSkills(skills, "iss-t").map((s) => s.name)).toEqual(["issue-to-pr"]);
  });
  it("ranks a word-boundary hit above a mid-word substring", () => {
    expect(filterSkills(skills, "rev").map((s) => s.name)).toEqual(["code-review", "prereview"]);
  });
  it("keeps the plain prefix rank on top and the substring/description ranks below", () => {
    expect(filterSkills(skills, "iss").map((s) => s.name)).toEqual([
      "issue-rounds",
      "issue-to-pr",
      "fewer-permission-prompts",
      "security-assessment",
    ]);
  });
  it("keeps own skills ahead of CLI-bundled ones even when the bundled one skips fewer words", () => {
    const cli = (name: string): SessionSkill => ({ ...sk(name), source: "cli" });
    const list = [cli("t_p"), sk("issue-to-pr")];
    expect(filterSkills(list, "t-p").map((s) => s.name)).toEqual(["issue-to-pr", "t_p"]);
  });
  it("accepts an all-caps query (Caps Lock on)", () => {
    for (const q of ["ISST", "ISS-T", "ITP"]) expect(wordMatchCost("issue-to-pr", q)).toBe(0);
    expect(filterSkills(skills, "ISS-T").map((s) => s.name)).toEqual(["issue-to-pr"]);
  });
  it("keeps input order for equal rank, tier and cost", () => {
    const list = [sk("b-x"), sk("a-x"), sk("c-x")];
    expect(filterSkills(list, "x").map((s) => s.name)).toEqual(["b-x", "a-x", "c-x"]);
  });
  it("prefers fewer skipped words within the word rank", () => {
    const list = [sk("issue-to-pr"), sk("to-pr-helper")];
    expect(filterSkills(list, "t-p").map((s) => s.name)).toEqual(["to-pr-helper", "issue-to-pr"]);
  });
});

describe("collapseCli / splitDoubleTrigger (two-tier display, docs/log/50 §9)", () => {
  const cli = (name: string): SessionSkill => ({ ...sk(name), source: "cli" });
  it("folds the CLI-bundled entries away behind a count when the user has entries of their own", () => {
    const items = [sk("handoff"), cli("dataviz"), cli("simplify")];
    expect(collapseCli(items, false)).toEqual({ shown: [items[0]], hidden: 2 });
    expect(collapseCli(items, true)).toEqual({ shown: items, hidden: 0 });
  });
  it("shows a list made of CLI entries alone as it is (nothing to rank above them)", () => {
    const items = [cli("dataviz"), cli("simplify")];
    expect(collapseCli(items, false)).toEqual({ shown: items, hidden: 0 });
    expect(collapseCli([], false)).toEqual({ shown: [], hidden: 0 });
  });
  it("treats a token that starts with the trigger again (// or $$) as the show-all gesture", () => {
    expect(splitDoubleTrigger("/", "/")).toEqual({ query: "", all: true });
    expect(splitDoubleTrigger("/sim", "/")).toEqual({ query: "sim", all: true });
    expect(splitDoubleTrigger("／", "/")).toEqual({ query: "", all: true }); // full-width alias
    expect(splitDoubleTrigger("$ima", "$")).toEqual({ query: "ima", all: true });
    expect(splitDoubleTrigger("sim", "/")).toEqual({ query: "sim", all: false });
    expect(splitDoubleTrigger("", "/")).toEqual({ query: "", all: false });
    expect(splitDoubleTrigger("/x", "")).toEqual({ query: "/x", all: false }); // button-only kind: no trigger, no gesture
  });
  it("the double-trigger token is replaced whole on confirmation (no stray slash left behind)", () => {
    expect(applySkillToDraft("//", 2, "/simplify ")).toEqual({ next: "/simplify ", caret: 10 });
    expect(applySkillToDraft("//sim args", 5, "/simplify ")).toEqual({ next: "/simplify args", caret: 10 });
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
