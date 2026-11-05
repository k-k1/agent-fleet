import { describe, expect, it } from "vitest";
import {
  buildImagePrompt,
  samePastedPrompt,
  splitCommandArgs,
  splitPastedImages,
  FILE_PROMPT,
  FILE_PROMPT_GENERIC,
  IMG_PROMPT,
  IMG_PROMPT_LEGACY,
  quoteImagePaths,
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
  it("reads a muse user turn: the words, then the bare image paths", () => {
    // The Agent's muse transcript replaces the host's [Image #N] placeholders with the paths it
    // sent (agents/muse withImagePaths): no instruction, no marker, space-separated.
    const landed = `sticky にできないか ${P1} ${P2}`;
    expect(splitPastedImages(landed)).toEqual({ text: "sticky にできないか", images: ["paste-1.png", "paste-2.jpg"], files: [] });
    expect(samePastedPrompt(landed, "sticky にできないか")).toBe(true);
    expect(splitPastedImages(P1)).toEqual({ text: "", images: ["paste-1.png"], files: [] });
  });
  it("reads a claude turn whose attachment paths the Agent wrapped in backticks", () => {
    // The Agent quotes them before typing so claude's paste handler leaves them as text
    // (agents/claude QuoteImagePaths); the echo the Console holds has them bare.
    const landed = `見て ${FILE_PROMPT} \`${P1}\` \`${P2}\``;
    expect(splitPastedImages(landed)).toEqual({ text: "見て", images: ["paste-1.png", "paste-2.jpg"], files: [] });
    expect(samePastedPrompt(landed, buildImagePrompt("見て", [P1, P2]))).toBe(true);
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

describe("samePastedPrompt", () => {
  it("matches the copy claude logs after dropping invisible characters and reshaping whitespace", () => {
    // A paste copied from a chat app or a table: BOM, zero-width space, bidi mark, variation
    // selector, NBSP, tabs and CRLF. claude removes the invisible ones; the Agent sends LF.
    const sent = "\ufeff今の状態\t求められる確認\r\n\u00a0\r\n番号が\u200b登録済み\u200e\t両方\ufe0f";
    const logged = "今の状態    求められる確認\n \n番号が登録済み    両方";
    expect(samePastedPrompt(logged, sent)).toBe(true);
    expect(samePastedPrompt("番号が\u2060未登録\u{e0041}", "番号が未登録")).toBe(true);
  });
  it("still tells different visible text apart", () => {
    expect(samePastedPrompt("番号が登録済み", "番号が未登録")).toBe(false);
    expect(samePastedPrompt("a\u200bb", "a-b")).toBe(false);
    // Whitespace is collapsed, not dropped: these are two different prompts.
    expect(samePastedPrompt("a b", "ab")).toBe(false);
    expect(samePastedPrompt("x = 1", "x=1")).toBe(false);
  });
});

describe("splitCommandArgs", () => {
  const none = (args: string) => ({ text: args, images: [], files: [] });

  it("splits the appended instruction and paths, raw or backticked", () => {
    expect(splitCommandArgs(buildImagePrompt("直して", [P1, F1]))).toEqual({ text: "直して", images: ["paste-1.png"], files: ["paste-3-server.log"] });
    expect(splitCommandArgs(`直して ${FILE_PROMPT} \`${P1}\` \`${P2}\``)).toEqual({
      text: "直して",
      images: ["paste-1.png", "paste-2.jpg"],
      files: [],
    });
    expect(splitCommandArgs(buildImagePrompt("", [P1]))).toEqual({ text: "", images: ["paste-1.png"], files: [] });
  });

  it("collects only the paths after the last instruction", () => {
    const head = `${P1} と "${FILE_PROMPT}" を比べて`;
    expect(splitCommandArgs(`${head} ${FILE_PROMPT} \`${P2}\``)).toEqual({ text: head, images: ["paste-2.jpg"], files: [] });
  });

  it("keeps args whole when no generated attachment tail is there", () => {
    for (const args of [
      "review 1110",
      `${P1} を調べて`,
      `"${FILE_PROMPT}" という指示を検討して`,
      `"${FILE_PROMPT}" は ${P1} に使う指示です`,
      `${FILE_PROMPT} ${P1} を開いて`,
    ])
      expect(splitCommandArgs(args)).toEqual(none(args));
  });
});

// The cases of TestQuoteImagePaths (workspace/agent/internal/agents/claude/image_paste_test.go),
// so the twin cannot drift from the Go rewrite. The studio-signal case is left out: the caller
// strips that line first.
describe("quoteImagePaths", () => {
  const p1 = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png";
  const p2 = "/home/dev/.cache/agent-fleet/pasted/sid/paste-2.JPG";
  const cases: [string, string, string][] = [
    ["no attachment line", "curl -o /dev/null /tmp/x.png", "curl -o /dev/null /tmp/x.png"],
    [
      "paths after the instruction",
      "確認して Open the following file(s) with the Read tool: " + p1 + " " + p2,
      "確認して Open the following file(s) with the Read tool: `" + p1 + "` `" + p2 + "`",
    ],
    [
      "words before the instruction untouched",
      "see /tmp/a.png Open the following file(s) with the Read tool: " + p1,
      "see /tmp/a.png Open the following file(s) with the Read tool: `" + p1 + "`",
    ],
    [
      "memo flush shape",
      "memo\n\nOpen the following file(s) with the Read tool: " + p1 + "\n",
      "memo\n\nOpen the following file(s) with the Read tool: `" + p1 + "`\n",
    ],
    [
      "non-image attachment left alone",
      "Open the following file(s) with the Read tool: /x/pasted/s/paste-3-report.pdf",
      "Open the following file(s) with the Read tool: /x/pasted/s/paste-3-report.pdf",
    ],
    [
      "legacy image wording",
      "Open the following image(s) with the Read tool: " + p1,
      "Open the following image(s) with the Read tool: `" + p1 + "`",
    ],
    ["partial instruction left alone", "Inspect with the Read tool: /tmp/a.png", "Inspect with the Read tool: /tmp/a.png"],
    [
      "text after the paths means it is not the attachment line",
      "Open the following file(s) with the Read tool: " + p1 + " and compare",
      "Open the following file(s) with the Read tool: " + p1 + " and compare",
    ],
    [
      "full instruction with a path that is not an upload",
      "Open the following file(s) with the Read tool: /tmp/a.png",
      "Open the following file(s) with the Read tool: /tmp/a.png",
    ],
    [
      "memo images",
      "Open the following file(s) with the Read tool: /home/dev/.cache/agent-fleet/memo-images/paste-9.png\n",
      "Open the following file(s) with the Read tool: `/home/dev/.cache/agent-fleet/memo-images/paste-9.png`\n",
    ],
    [
      "CRLF and tabs kept",
      "x\r\nOpen the following file(s) with the Read tool:\t" + p1 + "\r\n",
      "x\r\nOpen the following file(s) with the Read tool:\t`" + p1 + "`\r\n",
    ],
    [
      // strings.Fields splits on any Unicode space, the rewrite only on ASCII ones.
      "NBSP before the path",
      "Open the following file(s) with the Read tool:\u00a0" + p1,
      "Open the following file(s) with the Read tool:`\u00a0" + p1 + "`",
    ],
    [
      "vertical tab before the path",
      "Open the following file(s) with the Read tool:\v" + p1,
      "Open the following file(s) with the Read tool:`\v" + p1 + "`",
    ],
  ];
  it.each(cases)("%s", (_name, input, want) => {
    expect(quoteImagePaths(input)).toBe(want);
    expect(quoteImagePaths(want)).toBe(want);
  });
});
