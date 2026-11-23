import { describe, expect, it, vi } from "vitest";

vi.mock("../../core/api/client.ts", () => ({ apiJSON: async () => ({}), api: async () => ({}), getTenant: () => "", getUser: () => "" }));
const { activeRepoOf } = await import("./useRepoRail.ts");

// ADR 0142 decision 9: a space session that shares a repository's folder name must not select
// that repository's row.
describe("activeRepoOf", () => {
  it("is the repository of a session in one", () => {
    expect(activeRepoOf({ name: "a", kind: "claude", repo: "notes" })).toBe("notes");
  });
  it("is nothing for a session in a space of the same name", () => {
    expect(activeRepoOf({ name: "b", kind: "claude", repo: "notes", space: "notes" })).toBeNull();
  });
  it("is nothing without a session", () => {
    expect(activeRepoOf(undefined)).toBeNull();
  });
});
