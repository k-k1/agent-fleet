import { describe, expect, it } from "vitest";
import { hiddenModelsFor, isModelHidden, modelMatchesHidden } from "./modelDeny.ts";

// The matching rule is paired with the Agent's workspace/agent/model_deny.go; both sides carry the
// same cases. If they drift, a model shows up in the picker but launching it returns 400.
describe("hidden model matching", () => {
  it("matches an alias inside a full model id, on token boundaries", () => {
    expect(modelMatchesHidden("fable", "fable")).toBe(true);
    expect(modelMatchesHidden("Fable", "fable")).toBe(true);
    expect(modelMatchesHidden("claude-fable-5", "fable")).toBe(true);
    expect(modelMatchesHidden("claude-opus-5", "fable")).toBe(false);
    // a mere substring must not match
    expect(modelMatchesHidden("fablet", "fable")).toBe(false);
    expect(modelMatchesHidden("unfable", "fable")).toBe(false);
  });

  it("keeps the opencode billing routes apart", () => {
    expect(modelMatchesHidden("opencode-go/glm-5.2", "opencode/glm-5.2")).toBe(false);
    expect(modelMatchesHidden("opencode/glm-5.2", "opencode/glm-5.2")).toBe(true);
    // The bare name is multi-token too, so it matches no family. Hiding both routes means
    // excluding both ids.
    expect(modelMatchesHidden("opencode-go/glm-5.2", "glm-5.2")).toBe(false);
  });

  // Hiding a concrete id (multiple tokens) must not take out other models that merely have it as
  // a prefix: hiding GPT-5.4 once removed mini as well.
  it("does not take out longer ids that merely start with an excluded id", () => {
    expect(modelMatchesHidden("gpt-5.4-mini", "gpt-5.4")).toBe(false);
    expect(modelMatchesHidden("gpt-5.4", "gpt-5.4")).toBe(true);
    expect(modelMatchesHidden("gpt-5.4-mini", "gpt-5.4-mini")).toBe(true);
    expect(modelMatchesHidden("claude-fable-5-20260101", "claude-fable-5")).toBe(false);
  });

  it("treats an empty model as 'not chosen' rather than hidden", () => {
    expect(isModelHidden({ claude: ["fable"] }, "claude", "")).toBe(false);
    expect(isModelHidden(undefined, "claude", "fable")).toBe(false);
    expect(isModelHidden({ claude: ["fable"] }, "codex", "fable")).toBe(false); // scoped per kind
  });

  it("ignores a config that would hide every claude tier", () => {
    const all = ["fable", "opus", "sonnet", "haiku"];
    expect(hiddenModelsFor({ claude: all }, "claude", all)).toEqual([]);
    expect(isModelHidden({ claude: all }, "claude", "fable", all)).toBe(false);
    // as long as one tier is left, exclusion works as usual
    expect(isModelHidden({ claude: ["fable"] }, "claude", "fable", all)).toBe(true);
  });

  it("drops junk entries", () => {
    expect(hiddenModelsFor({ claude: [" ", "fable"] }, "claude")).toEqual(["fable"]);
    expect(hiddenModelsFor({ claude: "fable" as unknown as string[] }, "claude")).toEqual([]);
  });
});
