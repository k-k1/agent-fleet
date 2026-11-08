import { describe, expect, it } from "vitest";
import { afDrawioFrames } from "../../../scripts/afDrawioFrames.js";
import { drawioFrameFile } from "./drawioFrame.ts";

// The frame is a built file because a srcdoc would inherit the Console's script-src 'self'.
describe("drawio frame files", () => {
  it("the build plugin emits exactly the files the view points the iframe at", async () => {
    const emitted: { fileName: string; source: string }[] = [];
    const ctx = { addWatchFile() {}, emitFile: (f: { fileName: string; source: string }) => void emitted.push(f) };
    await (afDrawioFrames().generateBundle as (this: typeof ctx) => Promise<void>).call(ctx);
    expect(emitted.map((e) => e.fileName).sort()).toEqual([drawioFrameFile(false), drawioFrameFile(true)].sort());
    for (const e of emitted) {
      expect(e.source).toContain("<!doctype html>");
      expect(e.source).toContain("af-drawio");
    }
    expect(emitted.find((e) => e.fileName === drawioFrameFile(true))!.source).toContain("#1e1e1e");
  });

  it("names match what the Control Plane exempts and gives its own CSP", () => {
    expect([drawioFrameFile(false), drawioFrameFile(true)]).toEqual(["drawio-frame-light.html", "drawio-frame-dark.html"]);
  });
});
