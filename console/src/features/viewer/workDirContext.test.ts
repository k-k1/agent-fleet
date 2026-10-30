// Which sessions get a $AF_WORK_DIR expansion: every known session, Terminal or Managed.
import { describe, expect, it } from "vitest";
import { mirrorWorkDir } from "./workDirContext.ts";

describe("mirrorWorkDir", () => {
  it("expands for a Terminal session (driver absent or tui)", () => {
    expect(mirrorWorkDir("s123", { driver: undefined, kind: "opencode" })).toBe("~/.af-work/s123");
    expect(mirrorWorkDir("s123", { driver: "tui", kind: "opencode" })).toBe("~/.af-work/s123");
  });

  it("expands for a Managed session, whatever the kind", () => {
    for (const kind of ["codex", "cursor", "copilot", "kiro", "muse", "lcpp", "opencode"] as const) {
      expect(mirrorWorkDir("s123", { driver: "managed", kind })).toBe("~/.af-work/s123");
    }
  });

  it("stays empty when the session is unknown", () => {
    expect(mirrorWorkDir("s123", null)).toBe("");
    expect(mirrorWorkDir("s123", undefined)).toBe("");
  });
});
