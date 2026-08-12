import { describe, expect, it } from "vitest";
import {
  buildImagePrompt,
  samePastedPrompt,
  splitPastedImages,
  FILE_PROMPT,
  FILE_PROMPT_GENERIC,
  IMG_PROMPT,
  IMG_PROMPT_LEGACY,
} from "./pastedImages.ts";

const P1 = "/home/u/.cache/agent-fleet/pasted/abc/paste-1.png";
const P2 = "/home/u/.cache/agent-fleet/pasted/abc/paste-2.jpg";
const F1 = "/home/u/.cache/agent-fleet/pasted/abc/paste-3-server.log";
const F2 = "/home/u/.cache/agent-fleet/pasted/abc/paste-4-spec_v2.pdf";

describe("buildImagePrompt", () => {
  it("claude gets the Read-tool wording (and stays the default)", () => {
    expect(buildImagePrompt("見て", [P1])).toBe(`見て ${FILE_PROMPT} ${P1}`);
    expect(buildImagePrompt("見て", [P1], "claude")).toBe(`見て ${FILE_PROMPT} ${P1}`);
  });
  it("codex/opencode get the tool-neutral wording", () => {
    expect(buildImagePrompt("見て", [P1], "codex")).toBe(`見て ${FILE_PROMPT_GENERIC} ${P1}`);
    expect(buildImagePrompt("", [P1, F1], "opencode")).toBe(`${FILE_PROMPT_GENERIC} ${P1} ${F1}`);
  });
  it("no paths → the text unchanged", () => {
    expect(buildImagePrompt("そのまま", [], "codex")).toBe("そのまま");
  });
});

describe("splitPastedImages", () => {
  it("strips each known instruction wording and classifies images vs files", () => {
    for (const kind of ["claude", "codex"]) {
      const { text, images, files } = splitPastedImages(buildImagePrompt("これを確認", [P1, F1, P2, F2], kind));
      expect(text).toBe("これを確認");
      expect(images).toEqual(["paste-1.png", "paste-2.jpg"]);
      expect(files).toEqual(["paste-3-server.log", "paste-4-spec_v2.pdf"]);
    }
  });
  it("still strips the older image-only wordings", () => {
    for (const instr of [IMG_PROMPT, IMG_PROMPT_LEGACY]) {
      const { text, images, files } = splitPastedImages(`古い ${instr} ${P1}`);
      expect(text).toBe("古い");
      expect(images).toEqual(["paste-1.png"]);
      expect(files).toEqual([]);
    }
  });
  it("plain text passes through", () => {
    expect(splitPastedImages("画像なし")).toEqual({ text: "画像なし", images: [], files: [] });
  });
  it("strips Codex localImage markers while retaining thumbnails", () => {
    const raw = `確認して <image name=[Image #1] path="${P1}"> <image name=[Image #2] path="${P2}">`;
    expect(splitPastedImages(raw)).toEqual({
      text: "確認して",
      images: ["paste-1.png", "paste-2.jpg"],
      files: [],
    });
  });
  it("matches a Codex image-bearing rollout turn to its optimistic echo", () => {
    const landed = `確認して <image name=[Image #1] path="${P1}">`;
    expect(samePastedPrompt(landed, "確認して")).toBe(true);
    expect(samePastedPrompt(landed, "別の依頼")).toBe(false);
  });
  it("strips a trailing bare </image> closing tag (captioned Codex send)", () => {
    // Real rollout shape for a captioned managed-Codex send: input_text items are
    // concatenated with no separator, so the closing tag glues directly onto the
    // opening tag/caption with no surrounding whitespace.
    const landed = `確認して<image name=[Image #1] path="${P1}"></image>`;
    expect(splitPastedImages(landed)).toEqual({
      text: "確認して",
      images: ["paste-1.png"],
      files: [],
    });
    expect(samePastedPrompt(landed, "確認して")).toBe(true);
  });
});
