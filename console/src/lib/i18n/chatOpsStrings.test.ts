// Chat-bridge / ops-monitoring wording that must agree with the code (#3667).
import { describe, it, expect } from "vitest";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

describe.each([
  ["en", en],
  ["ja", ja],
] as const)("%s chat/ops strings", (_name, c) => {
  const s = c as Record<string, string>;
  it("Slack has its own test-sent toast that names Slack, not Discord", () => {
    expect(s["ops.sl_test_sent"]).toContain("Slack");
    expect(s["ops.sl_test_sent"]).not.toContain("Discord");
  });
  it("hints state that full-text mode posts the answer body (not 'display name and state only')", () => {
    for (const k of ["ops.dc_hint", "ops.sl_hint"]) {
      expect(s[k]).toMatch(/Full-text|全文/);
    }
  });
  it("Slack receive sub mentions plan approvals", () => {
    expect(s["ops.sl_receive_sub"]).toMatch(/plan|プラン/);
  });
  it("SRE description and MCP hint include AWS", () => {
    expect(s["assistant.sre.desc"]).toContain("AWS");
    expect(s["asst.mcp_hint"]).toContain("AWS");
  });
});
