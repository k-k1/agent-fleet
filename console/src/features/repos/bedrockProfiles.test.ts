import { describe, expect, it, vi } from "vitest";

// The module's hook fetches through the API client, which reads the browser's storage on load.
vi.mock("../../core/api/client.ts", () => ({ apiJSON: async () => ({}), api: vi.fn() }));
const { bedrockConnection, bedrockModelOk, markedProfiles, regionOk, routeModelFilter, routeSelectionBody } = await import("./bedrockProfiles.ts");

describe("bedrockProfiles", () => {
  it("keeps only marked, well-formed profiles", () => {
    expect(
      markedProfiles([
        { id: "a", label: "A", region: "us-east-1", modelRoute: true },
        { id: "b", label: "B", region: "us-east-1" },
        { id: "c", label: "C", modelRoute: false },
        { id: "d/e", modelRoute: true },
        { modelRoute: true },
        { id: "f", modelRoute: true },
      ]),
    ).toEqual([
      { id: "a", label: "A", region: "us-east-1" },
      { id: "f", label: "f", region: "" },
    ]);
    expect(markedProfiles({ error: "x" })).toEqual([]);
  });
  it("writes the connection reference and checks a region", () => {
    expect(bedrockConnection("p1", " eu-west-1 ")).toBe("p1@eu-west-1");
    expect(bedrockConnection("p1", "")).toBe("p1@");
    expect(regionOk("")).toBe(true);
    expect(regionOk("ap-northeast-1")).toBe(true);
    expect(regionOk("us-gov-west-1")).toBe(true);
    expect(regionOk("cn-north-1")).toBe(false);
    expect(regionOk("not a region")).toBe(false);
  });
  it("puts the connection in the create body only when there is one", () => {
    expect(routeSelectionBody("bedrock", "p1@eu-west-1")).toEqual({ route: "bedrock", connection: "p1@eu-west-1" });
    expect(routeSelectionBody("anthropic-api")).toEqual({ route: "anthropic-api" });
    expect(routeSelectionBody("anthropic-api", "")).toEqual({ route: "anthropic-api" });
  });

  it("narrows opencode's models to the chosen route", () => {
    const ids = ["", "opencode/big-pickle", "amazon-bedrock/anthropic.claude-sonnet-4-6", "amazon-bedrock/"];
    // bedrock chosen: its models only, and not opencode's default ("").
    expect(ids.filter(routeModelFilter("opencode", "bedrock")!)).toEqual(["amazon-bedrock/anthropic.claude-sonnet-4-6"]);
    // not chosen, whatever the reason (none picked, not offered, Managed, loading): never the route's models.
    expect(ids.filter(routeModelFilter("opencode", "")!)).toEqual(["", "opencode/big-pickle"]);
    // a kind with no route-only models: no narrowing.
    expect(routeModelFilter("claude", "bedrock")).toBeUndefined();
  });

  // The same cases as routes.TestParseBedrockModelID / TestParseBedrockAnthropicModelID: the
  // Console and the Agent have to agree, or a launch the dialog allows is refused.
  it("accepts Anthropic Bedrock ids only", () => {
    for (const ok of [
      "anthropic.claude-opus-4-6-v1",
      "us.anthropic.claude-opus-4-6-v1",
      "eu.anthropic.claude-opus-5-5",
      "global.anthropic.claude-opus-5-5",
      "us-gov.anthropic.claude-sonnet-4-5-20250929-v1:0",
      "apac.anthropic.claude-sonnet-4-5-20250929-v1:0:200k",
      "jp.anthropic.claude-haiku-4-5-20251001-v1:0",
    ])
      expect(bedrockModelOk(ok), ok).toBe(true);
    for (const bad of [
      "",
      " us.anthropic.claude-opus-4-6-v1",
      "opus",
      "claude-opus-4-6",
      "us.anthropic.claude-opus-4-6-v1[1m]",
      "arn:aws:bedrock:us-east-1:000000000000:application-inference-profile/abc",
      "us.anthropic/claude",
      "anthropic.claude-opus-4-6-v1:",
      "anthropic.claude-opus-4-6-v1:x",
      "anthropic.claude-opus-4-6-v1:0:200",
      "US.anthropic.claude-opus-4-6-v1",
      "us.eu.claude",
      "anthropic",
      "global.openai.gpt-oss-120b-1:0",
      "amazon.nova-pro-v1:0",
      "x".repeat(201),
    ])
      expect(bedrockModelOk(bad), bad).toBe(false);
  });
});
