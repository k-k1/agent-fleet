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
  it("goes by the stored place (#5219)", () => {
    expect(activeRepoOf({ name: "c", kind: "claude", repo: "notes", place: { kind: "space", id: "notes" } })).toBeNull();
    expect(activeRepoOf({ name: "d", kind: "claude", repo: "dev", place: { kind: "home" } })).toBeNull();
    expect(activeRepoOf({ name: "e", kind: "claude", repo: "app", place: { kind: "other" } })).toBeNull();
    expect(activeRepoOf({ name: "f", kind: "claude", repo: "app@wip", place: { kind: "worktree", id: "app@wip", parent: "app" } })).toBe("app@wip");
  });
  it("is nothing without a session", () => {
    expect(activeRepoOf(undefined)).toBeNull();
  });
});
