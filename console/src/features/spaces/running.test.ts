import { describe, expect, it } from "vitest";
import { busySpaces, runningInSpace } from "./running.ts";
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

  it("lists the spaces with one", () => {
    expect([...busySpaces(sessions)]).toEqual(["b"]);
    expect(busySpaces([...sessions, ses("ssh", "ssm", "c", true)]).has("c")).toBe(false);
  });
});
