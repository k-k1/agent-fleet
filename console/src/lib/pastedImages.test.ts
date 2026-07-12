import { describe, expect, it } from "vitest";
import { buildImagePrompt, splitPastedImages, IMG_PROMPT, IMG_PROMPT_GENERIC, IMG_PROMPT_LEGACY } from "./pastedImages.ts";

const P1 = "/home/u/.cache/agent-fleet/pasted/abc/paste-1.png";
const P2 = "/home/u/.cache/agent-fleet/pasted/abc/paste-2.jpg";

describe("buildImagePrompt", () => {
  it("claude gets the Read-tool wording (and stays the default)", () => {
    expect(buildImagePrompt("見て", [P1])).toBe(`見て ${IMG_PROMPT} ${P1}`);
    expect(buildImagePrompt("見て", [P1], "claude")).toBe(`見て ${IMG_PROMPT} ${P1}`);
  });
  it("codex/opencode get the tool-neutral wording", () => {
    expect(buildImagePrompt("見て", [P1], "codex")).toBe(`見て ${IMG_PROMPT_GENERIC} ${P1}`);
    expect(buildImagePrompt("", [P1, P2], "opencode")).toBe(`${IMG_PROMPT_GENERIC} ${P1} ${P2}`);
  });
  it("no paths → the text unchanged", () => {
    expect(buildImagePrompt("そのまま", [], "codex")).toBe("そのまま");
  });
});

describe("splitPastedImages", () => {
  it("strips each known instruction wording and returns the basenames", () => {
    for (const kind of ["claude", "codex"]) {
      const { text, images } = splitPastedImages(buildImagePrompt("これを確認", [P1, P2], kind));
      expect(text).toBe("これを確認");
      expect(images).toEqual(["paste-1.png", "paste-2.jpg"]);
    }
  });
  it("still strips the legacy wording", () => {
    const { text, images } = splitPastedImages(`古い ${IMG_PROMPT_LEGACY} ${P1}`);
    expect(text).toBe("古い");
    expect(images).toEqual(["paste-1.png"]);
  });
  it("plain text passes through", () => {
    expect(splitPastedImages("画像なし")).toEqual({ text: "画像なし", images: [] });
  });
});
