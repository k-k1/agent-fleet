import { describe, expect, it } from "vitest";
import {
  buildImagePrompt,
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
});
