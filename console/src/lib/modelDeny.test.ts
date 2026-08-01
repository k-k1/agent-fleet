import { describe, expect, it } from "vitest";
import { hiddenModelsFor, isModelHidden, modelMatchesHidden } from "./modelDeny.ts";

// 判定規則は Agent 側 workspace/agent/model_deny.go と対（同じケースを両側で持つ）。
// ここがズレると「ピッカーには出るのに起動は 400」という食い違いになる。
describe("hidden model matching", () => {
  it("matches an alias inside a full model id, on token boundaries", () => {
    expect(modelMatchesHidden("fable", "fable")).toBe(true);
    expect(modelMatchesHidden("Fable", "fable")).toBe(true);
    expect(modelMatchesHidden("claude-fable-5", "fable")).toBe(true);
    expect(modelMatchesHidden("claude-opus-5", "fable")).toBe(false);
    // 単なる部分文字列では当てない
    expect(modelMatchesHidden("fablet", "fable")).toBe(false);
    expect(modelMatchesHidden("unfable", "fable")).toBe(false);
  });

  it("keeps the opencode billing routes apart", () => {
    expect(modelMatchesHidden("opencode-go/glm-5.2", "opencode/glm-5.2")).toBe(false);
    expect(modelMatchesHidden("opencode/glm-5.2", "opencode/glm-5.2")).toBe(true);
    // 素の名前も複数トークンなので族一致はしない。両経路を隠すなら両方の id を除外する。
    expect(modelMatchesHidden("opencode-go/glm-5.2", "glm-5.2")).toBe(false);
  });

  // 具体 id（複数トークン）を隠しても、それを接頭辞に持つ別モデルは巻き添えにしない。
  // GPT-5.4 を隠したら mini まで消えた不具合の回帰。
  it("does not take out longer ids that merely start with an excluded id", () => {
    expect(modelMatchesHidden("gpt-5.4-mini", "gpt-5.4")).toBe(false);
    expect(modelMatchesHidden("gpt-5.4", "gpt-5.4")).toBe(true);
    expect(modelMatchesHidden("gpt-5.4-mini", "gpt-5.4-mini")).toBe(true);
    expect(modelMatchesHidden("claude-fable-5-20260101", "claude-fable-5")).toBe(false);
  });

  it("treats an empty model as 'not chosen' rather than hidden", () => {
    expect(isModelHidden({ claude: ["fable"] }, "claude", "")).toBe(false);
    expect(isModelHidden(undefined, "claude", "fable")).toBe(false);
    expect(isModelHidden({ claude: ["fable"] }, "codex", "fable")).toBe(false); // kind スコープ
  });

  it("ignores a config that would hide every claude tier", () => {
    const all = ["fable", "opus", "sonnet", "haiku"];
    expect(hiddenModelsFor({ claude: all }, "claude", all)).toEqual([]);
    expect(isModelHidden({ claude: all }, "claude", "fable", all)).toBe(false);
    // 1つでも残っていれば通常どおり効く
    expect(isModelHidden({ claude: ["fable"] }, "claude", "fable", all)).toBe(true);
  });

  it("drops junk entries", () => {
    expect(hiddenModelsFor({ claude: [" ", "fable"] }, "claude")).toEqual(["fable"]);
    expect(hiddenModelsFor({ claude: "fable" as unknown as string[] }, "claude")).toEqual([]);
  });
});
