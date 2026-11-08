import { describe, it, expect } from "vitest";
import { isClaudeAuthExpired, CLAUDE_AUTH_EXPIRED_FRAGMENT } from "./claudeAuthError.ts";

describe("isClaudeAuthExpired", () => {
  it("matches the measured CLI error", () => {
    expect(
      isClaudeAuthExpired("claude returned an error: Failed to authenticate: OAuth session expired and could not be refreshed"),
    ).toBe(true);
    expect(isClaudeAuthExpired("OAUTH SESSION EXPIRED")).toBe(true);
    expect(CLAUDE_AUTH_EXPIRED_FRAGMENT).toBe("oauth session expired");
  });
  it("does not misclassify unrelated errors", () => {
    for (const t of [
      "claude returned an error: Prompt is too long",
      "Failed to authenticate: network unreachable",
      "You've hit your session limit",
      "",
      undefined,
      null,
    ]) {
      expect(isClaudeAuthExpired(t)).toBe(false);
    }
  });
});
