// The composer's wire body (ADR 0100 decision 5): the studio signal is the LAST line, after the
// TUI's woven attachment paths, and the echo never carries it.
import { describe, expect, it } from "vitest";
import { composerSend } from "./composerSend.ts";
import { STUDIO_SIGNAL_SUFFIX, stripStudioSignal } from "./transcript/model.ts";

const SIG = "[studio v4 · draft changed → get_image_studio]";

describe("composerSend", () => {
  it("TUI: the paths are woven in first, then the signal ends the wire", () => {
    const s = composerSend("darker", ["uploads/a.png"], "claude", false, SIG);
    expect(s.wire.endsWith(STUDIO_SIGNAL_SUFFIX)).toBe(true);
    expect(s.wire.split("\n").pop()).toBe(SIG);
    expect(s.wire).toContain("uploads/a.png");
    expect(s.wire.indexOf("uploads/a.png")).toBeLessThan(s.wire.indexOf(SIG));
    expect(stripStudioSignal(s.wire)).toBe(s.echo);
  });

  it("the echo is the member's words, without the signal", () => {
    const s = composerSend("darker", [], "codex", true, SIG);
    expect(s.echo).toBe("darker");
    expect(s.echo).not.toContain("[studio");
    expect(s.attachments).toEqual([]);
  });

  it("Managed: attachments ride beside the text, and the signal still ends it", () => {
    const s = composerSend("darker", ["uploads/a.png"], "codex", true, SIG);
    expect(s.wire).toBe(`darker\n\n${SIG}`);
    expect(s.attachments).toEqual(["uploads/a.png"]);
  });

  it("no signal: the wire is the echo", () => {
    const s = composerSend("hi", [], "claude", false);
    expect(s.wire).toBe("hi");
  });
});
