import { describe, expect, it } from "vitest";
import { rankSpaces } from "./recent.ts";
import type { Session } from "../../types/session.ts";
import type { Space } from "./store.ts";

const sp = (folder: string, createdAt?: string): Space => ({ folder, name: "", dir: "/h/spaces/" + folder, createdAt });
const ses = (name: string, space: string | undefined, createdAt?: string): Session => ({ name, space, createdAt }) as Session;

describe("rankSpaces", () => {
  it("puts the space with the newest session first, whatever its own age", () => {
    const spaces = [sp("new-1", "2026-11-20T00:00:00Z"), sp("old-1", "2026-09-01T00:00:00Z")];
    const sessions = [ses("a", "old-1", "2026-11-23T10:00:00Z"), ses("b", "old-1", "2026-10-01T10:00:00Z")];
    expect(rankSpaces(spaces, sessions).map((s) => s.folder)).toEqual(["old-1", "new-1"]);
  });

  it("ranks a space with no session by its creation time", () => {
    const spaces = [sp("a-1", "2026-11-01T00:00:00Z"), sp("b-1", "2026-11-02T00:00:00Z"), sp("c-1")];
    expect(rankSpaces(spaces, []).map((s) => s.folder)).toEqual(["b-1", "a-1", "c-1"]);
  });

  it("ignores sessions outside a space and unparsable times, and does not mutate its input", () => {
    const spaces = [sp("a-1", "2026-11-01T00:00:00Z"), sp("b-1", "2026-11-02T00:00:00Z")];
    const sessions = [ses("repo", undefined, "2026-12-01T00:00:00Z"), ses("odd", "a-1", "not a date"), ses("none", "a-1")];
    expect(rankSpaces(spaces, sessions).map((s) => s.folder)).toEqual(["b-1", "a-1"]);
    expect(spaces.map((s) => s.folder)).toEqual(["a-1", "b-1"]);
  });

  it("breaks a tie by folder", () => {
    const at = "2026-11-01T00:00:00Z";
    expect(rankSpaces([sp("b-1", at), sp("a-1", at)], []).map((s) => s.folder)).toEqual(["a-1", "b-1"]);
  });
});
