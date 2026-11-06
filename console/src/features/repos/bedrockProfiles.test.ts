import { describe, expect, it, vi } from "vitest";

// The module's hook fetches through the API client, which reads the browser's storage on load.
vi.mock("../../core/api/client.ts", () => ({ api: vi.fn() }));
const { bedrockConnection, markedProfiles, regionOk, routeSelectionBody } = await import("./bedrockProfiles.ts");

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
});
