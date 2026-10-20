// Which sessions get a $AF_WORK_DIR expansion: Terminal only. A Managed session's directory is
// named after its working copy, so the session-named one must never be linked for it.
import { describe, expect, it } from "vitest";
import { mirrorWorkDir } from "./workDirContext.ts";

describe("mirrorWorkDir", () => {
  it("expands for a Terminal session (driver absent or tui)", () => {
    expect(mirrorWorkDir("s123", { driver: undefined })).toBe("~/.af-work/s123");
    expect(mirrorWorkDir("s123", { driver: "tui" })).toBe("~/.af-work/s123");
  });

  it("stays empty for a Managed session, so a stale ~/.af-work/s123/a.png is not linked", () => {
    expect(mirrorWorkDir("s123", { driver: "managed" })).toBe("");
  });

  it("stays empty when the session is unknown", () => {
    expect(mirrorWorkDir("s123", null)).toBe("");
    expect(mirrorWorkDir("s123", undefined)).toBe("");
  });
});
