import { describe, expect, it } from "vitest";
import { prettyModel } from "./modelName.ts";

describe("prettyModel", () => {
  it("shortens claude ids", () => {
    expect(prettyModel("claude-opus-4-8")).toBe("opus 4.8");
    expect(prettyModel("claude-sonnet-5")).toBe("sonnet 5");
  });

  it("drops the release date the API reports on live turns", () => {
    // What claude's own stream events name — the chat records this verbatim.
    expect(prettyModel("claude-sonnet-5-20260501")).toBe("sonnet 5");
    expect(prettyModel("claude-sonnet-4-5-20250929")).toBe("sonnet 4.5");
  });

  it("leaves other vendors' ids alone", () => {
    expect(prettyModel("gpt-5.6-codex")).toBe("gpt-5.6-codex");
    expect(prettyModel("opencode-go/glm-5.2")).toBe("opencode-go/glm-5.2");
    expect(prettyModel("Gemini 3.1 Pro (High)")).toBe("Gemini 3.1 Pro (High)");
  });
});
