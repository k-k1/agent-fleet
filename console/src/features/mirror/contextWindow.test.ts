import { describe, it, expect } from "vitest";
import { contextWindow } from "./ContextBar.tsx";

// Regression test paired with contextWindowGuess() on the Go side
// (workspace/agent/session_usage.go). Reading claude-opus-5 as 200k came from enumerating the
// 1M models; the rule is now inverted (1M by default, only the 200k models enumerated), so this
// also pins that an unknown future model falls to 1M and a missing entry cannot bring the bug
// back. When adding a model, change both sides.
describe("contextWindow", () => {
  it("Claude defaults to 1M, including unknown future models", () => {
    for (const m of [
      "claude-opus-5",
      "claude-sonnet-5",
      "claude-opus-4-8",
      "claude-opus-4-7",
      "claude-opus-4-6",
      "claude-sonnet-4-6",
      "claude-fable-5",
      "claude-mythos-5",
      "anthropic/claude-sonnet-5", // provider-prefixed, as opencode reports it
      "claude-opus-9", // unknown future model
    ]) {
      expect(contextWindow(m, 0), m).toBe(1000000);
    }
  });

  it("200k exceptions (generation 4-5 is not mistaken for 5)", () => {
    for (const m of [
      "claude-opus-4-5",
      "claude-sonnet-4-5-20250929",
      "claude-opus-4-1",
      "claude-opus-4-20250514", // id with a date suffix
      "claude-3-5-sonnet-20241022",
      "claude-3-7-sonnet",
      "claude-haiku-4-5-20251001",
    ]) {
      expect(contextWindow(m, 0), m).toBe(200000);
    }
  });

  it("GPT-5.x is 272k", () => {
    expect(contextWindow("gpt-5.1-codex", 0)).toBe(272000);
  });

  it("an unrecognised non-Claude model is 200k, raised to 1M once observed usage exceeds it", () => {
    expect(contextWindow("some-unknown-model", 0)).toBe(200000);
    expect(contextWindow("some-unknown-model", 250000)).toBe(1000000);
  });
});
