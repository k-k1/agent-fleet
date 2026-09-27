// The form draft's round trip (ADR 0081 decision 6) and what "open in image generation"
// does to it (decision 3).
//
// The reason this is tested at all: the draft comes back out of localStorage, which is
// untrusted input the same way a stored layout is. A repaired number here becomes a request
// the Agent refuses with 400, and the person cannot see which field lied.
import { describe, expect, it } from "vitest";
import {
  MAX_BATCH,
  MAX_JOBS,
  draftFromProperties,
  draftKey,
  draftParams,
  effectiveParams,
  emptyDraft,
  parseDraft,
  remappedOp,
  serializeDraft,
} from "./draft.ts";
import type { ImageProperties } from "./wire.ts";

describe("下書きの往復", () => {
  it("書いたものがそのまま戻る", () => {
    const d = {
      ...emptyDraft(),
      model: "sdxl-base",
      prompt: "1girl, solo",
      negative: "blurry",
      size: "1216x832",
      steps: "28",
      cfg: "6.5",
      sampler: "dpmpp_2m",
      scheduler: "karras",
      seedPolicy: "sequence" as const,
      seed: "1234",
      loras: [{ name: "detail", weight: 0.8 }],
      jobs: 40,
      batchSize: 2,
      op: "edit",
      inputs: ["generated/console/inputs/a.png"],
      strength: 0.45,
      outDir: "pics/keepers",
      label: "cfg sweep",
      fullSteps: true,
    };
    expect(parseDraft(serializeDraft(d))).toEqual(d);
  });

  it("読めない入力は空の下書きになる（例外にしない）", () => {
    expect(parseDraft(null)).toEqual(emptyDraft());
    expect(parseDraft("")).toEqual(emptyDraft());
    expect(parseDraft("{")).toEqual(emptyDraft());
    expect(parseDraft("[1,2]")).toEqual(emptyDraft());
    expect(parseDraft('"a string"')).toEqual(emptyDraft());
  });

  it("範囲外の数と知らない語は既定へ落とす", () => {
    const d = parseDraft(
      JSON.stringify({ jobs: 9999, batchSize: 99, seedPolicy: "clever", op: "upscale", size: "huge", strength: 5 }),
    );
    expect(d.jobs).toBe(MAX_JOBS);
    expect(d.batchSize).toBe(MAX_BATCH);
    expect(d.seedPolicy).toBe("random");
    expect(d.op).toBe("generate");
    expect(d.size).toBe("");
    expect(d.strength).toBe(1);
  });

  it("数の欄に入った非数値は捨てる（0 に直さない）", () => {
    // "28abc" repaired to 28 would silently run a different picture; empty means "the
    // model's default", which is what the placeholder already says.
    expect(parseDraft(JSON.stringify({ steps: "28abc", cfg: {} })).steps).toBe("");
    expect(parseDraft(JSON.stringify({ steps: "28abc", cfg: {} })).cfg).toBe("");
  });

  it("LoRA は name のある要素だけを残す", () => {
    const d = parseDraft(JSON.stringify({ loras: [{ name: "a", weight: 0.5 }, { weight: 1 }, null, { name: "b" }] }));
    expect(d.loras).toEqual([{ name: "a", weight: 0.5 }, { name: "b" }]);
  });

  it("鍵はワークスペースごと、無名でも安定する", () => {
    expect(draftKey("acme")).toBe("af.imagegen-draft.acme");
    expect(draftKey("")).toBe("af.imagegen-draft.default");
  });
});

// ADR 0094 決定 12: 編集専用の行へ切り替えても draft.op は残るので、候補に無くなった
// ときだけ読み替える。
describe("op の読み替え", () => {
  it("候補に無ければモデルの先頭の op に読み替える", () => {
    expect(remappedOp("generate", ["edit"])).toBe("edit");
  });

  it("候補にあれば読み替えない（null）", () => {
    expect(remappedOp("edit", ["generate", "edit", "inpaint"])).toBeNull();
  });

  it("ops が無ければ（古い Agent）読み替えない", () => {
    expect(remappedOp("generate", undefined)).toBeNull();
    expect(remappedOp("generate", [])).toBeNull();
  });
});

describe("params の重ね", () => {
  it("書かれた欄だけを送る", () => {
    expect(draftParams(emptyDraft())).toBeUndefined();
    expect(draftParams({ ...emptyDraft(), steps: "20" })).toEqual({ steps: 20 });
    expect(draftParams({ ...emptyDraft(), cfg: "0" })).toEqual({ cfg: 0 });
    expect(draftParams({ ...emptyDraft(), clipSkip: "2", guidance: "4.5", shift: "14" })).toEqual({
      clip_skip: 2,
      guidance: 4.5,
      shift: 14,
    });
    expect(draftParams({ ...emptyDraft(), guidance: "abc" })).toBeUndefined();
  });
});

describe("絵のプロパティを下書きへ読み込む", () => {
  const base = { ...emptyDraft(), prompt: "typed by hand", label: "my batch", jobs: 12 };

  it("復元できない絵は何も変えない", () => {
    expect(draftFromProperties(base, { source: "none" })).toEqual(base);
  });

  it("解決した欄だけを重ね、seed は固定にする", () => {
    const props: ImageProperties = {
      source: "png",
      model: "sdxl-base",
      prompt: "1girl",
      seed: 42,
      size: "1024x1024",
      // The sampler knobs arrive NESTED, in the request's own `params` shape.
      params: { steps: 28 },
      loras: [{ name: "detail", weight: 0.7 }],
    };
    const out = draftFromProperties(base, props);
    expect(out.model).toBe("sdxl-base");
    expect(out.prompt).toBe("1girl");
    expect(out.seed).toBe("42");
    expect(out.seedPolicy).toBe("fixed");
    expect(out.steps).toBe("28");
    expect(out.loras).toEqual([{ name: "detail", weight: 0.7 }]);
    // cfg/sampler were not recovered — the draft keeps what it had rather than blanking.
    expect(out.cfg).toBe(base.cfg);
    // The run's own fields are never restored: re-running one picture into someone's
    // 40-job label would be a surprise.
    expect(out.label).toBe("my batch");
    expect(out.jobs).toBe(12);
  });
});

// ADR 0081 決定 9: マスクはパス 1 本。壊れた値でも空文字に落ちるだけで、throw しない。
describe("マスク", () => {
  it("保存された draft から読み直せる", () => {
    expect(parseDraft(JSON.stringify({ mask: "generated/console/inputs/m.png" })).mask).toBe(
      "generated/console/inputs/m.png",
    );
  });

  it("空の draft では空文字で、文字列でない値も空文字になる", () => {
    expect(emptyDraft().mask).toBe("");
    expect(parseDraft(JSON.stringify({ mask: 7 })).mask).toBe("");
    expect(parseDraft("{}").mask).toBe("");
  });
});

// Review #9: the details summary reads the same rule as the request.
describe("effectiveParams", () => {
  const four = (k: string) => ["steps", "cfg", "sampler", "scheduler"].includes(k);
  const defaults = { steps: 28, cfg: 6, sampler: "dpmpp_2m", scheduler: "karras" };

  it("a value draftParams would send wins; the rest are the model's defaults", () => {
    const d = { ...emptyDraft(), cfg: "7", scheduler: "simple" };
    expect(effectiveParams(d, defaults, four)).toEqual([
      { key: "steps", value: 28, typed: false },
      { key: "cfg", value: 7, typed: true },
      { key: "sampler", value: "dpmpp_2m", typed: false },
      { key: "scheduler", value: "simple", typed: true },
    ]);
  });

  it("a value the request drops (not a number) shows the default, not the text", () => {
    expect(effectiveParams({ ...emptyDraft(), steps: "abc" }, defaults, four)[0]).toEqual({ key: "steps", value: 28, typed: false });
  });

  it("a knob the family does not read is left out; no default is null", () => {
    expect(effectiveParams(emptyDraft(), undefined, (k) => four(k) && k !== "sampler").map((p) => [p.key, p.value])).toEqual([
      ["steps", null],
      ["cfg", null],
      ["scheduler", null],
    ]);
  });

  it("#1035's knobs ride the same rule, after steps and cfg", () => {
    const d = { ...emptyDraft(), guidance: "4", clipSkip: "x" };
    expect(effectiveParams(d, { guidance: 3.5, shift: 3, clip_skip: 2 }, (k) => ["guidance", "shift", "clip_skip"].includes(k))).toEqual([
      { key: "guidance", value: 4, typed: true },
      { key: "shift", value: 3, typed: false },
      { key: "clip_skip", value: 2, typed: false },
    ]);
  });
});
