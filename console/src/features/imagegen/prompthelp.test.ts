// Layer B's message and its answer (ADR 0081 decision 7).
//
// `parseProposal` is the half that matters: a half-parsed prompt silently replacing what the
// person typed is exactly the silent edit the ADR forbids, so "unreadable" has to mean null
// and not "whatever could be salvaged".
import { describe, expect, it } from "vitest";
import { buildPromptHelpMessage, parseProposal } from "./prompthelp.ts";
import type { ImagegenLora, ImagegenModel } from "./wire.ts";

const sdxl: ImagegenModel = {
  id: "sdxl-base",
  family: "sdxl",
  description: "a general checkpoint",
  knobs: ["steps", "cfg", "sampler", "scheduler", "negative"],
  negative: "worst quality",
};

const lora: ImagegenLora = { name: "detail", baseModel: "sdxl", trained_words: ["add_detail"] };

describe("送る 1 通", () => {
  it("族の書き方・説明・トリガー語・行ネガティブ・利用者の意図が入る", () => {
    const msg = buildPromptHelpMessage({
      intent: "夕暮れの港",
      model: sdxl,
      loras: [lora],
      rowNegative: sdxl.negative,
      alwaysNegative: "nsfw",
      negativeReaches: true,
    });
    expect(msg).toContain("sdxl-base");
    expect(msg).toContain("a general checkpoint");
    expect(msg).toContain("danbooru-style tags");
    expect(msg).toContain("add_detail");
    expect(msg).toContain("worst quality");
    expect(msg).toContain("nsfw");
    expect(msg).toContain("夕暮れの港");
    expect(msg).toContain('{"prompt": "...", "negative": "...", "note": "..."}');
  });

  it("ネガティブが効かない族では空を返せと言う", () => {
    const msg = buildPromptHelpMessage({
      intent: "a harbour",
      model: { id: "k", family: "flux2-klein", knobs: ["steps"] },
      loras: [],
      negativeReaches: false,
    });
    expect(msg).toContain("ignores negative prompts");
    expect(msg).toContain("natural-language sentences");
  });
});

describe("返事の読み取り", () => {
  it("素の JSON", () => {
    expect(parseProposal('{"prompt":"a cat","negative":"blurry","note":"assumed daylight"}')).toEqual({
      prompt: "a cat",
      negative: "blurry",
      note: "assumed daylight",
    });
  });

  it("```json の囲いを剥がす", () => {
    expect(parseProposal('```json\n{"prompt":"a cat"}\n```')?.prompt).toBe("a cat");
  });

  it("前後に地の文があっても取れる", () => {
    expect(parseProposal('Sure! {"prompt":"a cat"} Hope that helps.')?.prompt).toBe("a cat");
  });

  it("読めない・prompt が無いものは null（部分適用しない）", () => {
    expect(parseProposal("")).toBeNull();
    expect(parseProposal("no json at all")).toBeNull();
    expect(parseProposal("{not json}")).toBeNull();
    expect(parseProposal('{"note":"I could not"}')).toBeNull();
    expect(parseProposal('{"prompt":"   "}')).toBeNull();
  });

  it("文字列でない欄は空に落とす", () => {
    expect(parseProposal('{"prompt":"a cat","negative":42}')).toEqual({ prompt: "a cat", negative: "", note: "" });
  });
});
