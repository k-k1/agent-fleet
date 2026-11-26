import { describe, expect, it } from "vitest";
import { busySpaces, runningInSpace, sessionInSpace } from "./running.ts";
import type { Session } from "../../types/session.ts";

const ses = (name: string, kind: string, space: string | undefined, alive: boolean): Session => ({ name, kind, space, alive }) as Session;

describe("runningInSpace / busySpaces", () => {
  const sessions = [
    ses("term", "shell", "a", true), // a person's terminal: not a second agent
    ses("old", "claude", "a", false), // stopped
    ses("repo", "codex", undefined, true), // not in a space
    ses("live", "codex", "b", true),
  ];

  it("finds a live AI session in the space and nothing else", () => {
    expect(runningInSpace(sessions, "a")).toBeUndefined();
    expect(runningInSpace(sessions, "b")?.name).toBe("live");
    expect(runningInSpace(sessions, "")).toBeUndefined();
    expect(runningInSpace(sessions, "c")).toBeUndefined();
  });

  it("goes by the stored place, on a row that carries no space (#5219)", () => {
    const placed = [
      { name: "p", kind: "codex", alive: true, repo: "c", place: { kind: "space", id: "c" } } as Session,
      { name: "r", kind: "codex", alive: true, repo: "d", space: "d", place: { kind: "repo", id: "d" } } as Session,
    ];
    expect(runningInSpace(placed, "c")?.name).toBe("p");
    expect([...busySpaces(placed)]).toEqual(["c"]);
  });

  it("lists the spaces with one", () => {
    expect([...busySpaces(sessions)]).toEqual(["b"]);
    expect(busySpaces([...sessions, ses("ssh", "ssm", "c", true)]).has("c")).toBe(false);
  });
});

describe("sessionInSpace", () => {
  const dir = "/home/dev/spaces/c";
  it("takes the stored place, the Agent's space, or a directory at or under the space's", () => {
    // a stored place alone, with a directory spelt another way than the space's own
    expect(sessionInSpace({ name: "p", place: { kind: "space", id: "c" }, dir: "/data/dev/spaces/c" } as Session, "c", dir)).toBe(true);
    expect(sessionInSpace({ name: "o", space: "c" } as Session, "c", dir)).toBe(true);
    expect(sessionInSpace({ name: "u", dir: dir + "/sub" } as Session, "c", dir)).toBe(true);
    expect(sessionInSpace({ name: "x", dir: dir + "x" } as Session, "c", dir)).toBe(false);
    // a repository named like the space is not in it
    expect(sessionInSpace({ name: "r", repo: "c", place: { kind: "repo", id: "c" }, dir: "/home/dev/repos/c" } as Session, "c", dir)).toBe(false);
  });

  it("goes by the Agent's current space over a stale stored place (#5278)", () => {
    const row = { name: "m", space: "b", place: { kind: "space", id: "a" }, dir: "/home/dev/links/current/.." } as Session;
    expect(sessionInSpace(row, "a", "/home/dev/spaces/a")).toBe(false);
    // the Agent's delete takes only what runs at or under the directory, so b does not claim it
    expect(sessionInSpace(row, "b", "/home/dev/spaces/b")).toBe(false);
  });
});
