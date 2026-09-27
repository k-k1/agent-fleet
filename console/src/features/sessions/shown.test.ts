import { describe, expect, it } from "vitest";
import { shownSession } from "./shown.ts";

const sessions = [{ name: "painter", studio: "st-1" }, { name: "plain" }];

describe("shownSession", () => {
  it("reads a session pane's own session", () => {
    expect(shownSession({ session: "plain", content: { kind: "terminal", chat: true } }, sessions)).toBe("plain");
  });

  it("resolves a studio pane to the session bound to that studio", () => {
    expect(shownSession({ session: null, content: { kind: "imagegen", studioId: "st-1" } }, sessions)).toBe("painter");
  });

  it("finds nothing for a studio-less, unbound or non-session pane", () => {
    expect(shownSession({ session: null, content: { kind: "imagegen", studioId: null } }, sessions)).toBe("");
    expect(shownSession({ session: null, content: { kind: "imagegen", studioId: "st-9" } }, sessions)).toBe("");
    expect(shownSession({ content: { kind: "sessions", showStopped: false } }, sessions)).toBe("");
    expect(shownSession(undefined, sessions)).toBe("");
  });
});

describe("shownSession: the terminal binding is not what is shown", () => {
  it("ignores the binding a studio or file pane keeps from the session it replaced", () => {
    expect(shownSession({ session: "plain", content: { kind: "imagegen", studioId: "st-1" } }, sessions)).toBe("painter");
    expect(shownSession({ session: "plain", content: { kind: "file", filePath: "a.md" } }, sessions)).toBe("");
  });
});
