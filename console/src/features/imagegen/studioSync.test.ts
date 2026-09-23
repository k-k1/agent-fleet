// The studio pane's pure half (ADR 0100): the form ↔ studio draft, the patch a debounce sends,
// the locks, the agent highlight, the versions, the signal line and the kind candidates.
import { describe, expect, it } from "vitest";
import { emptyDraft } from "./draft.ts";
import { stripStudioSignal, withStudioSignal } from "../mirror/transcript/model.ts";
import {
  agentTouched,
  changedKeys,
  describeChange,
  draftPatch,
  foldSince,
  formFromStudio,
  isLockable,
  mergeForm,
  pressSeqOf,
  rebaseForm,
  studioFromForm,
  studioKindChoices,
  studioSignal,
  toggleLock,
  versionsOf,
  worktreeOptional,
} from "./studioSync.ts";
import type { DraftLogEntry, StudioDraft } from "./wire.ts";

const words = { draftChanged: "draft changed", newResults: "{n} new results", rewind: "rewound to #{n}" };

describe("フォームとスタジオの下書き", () => {
  it("往復で失わない", () => {
    const d: StudioDraft = {
      provider: "comfy",
      model: "illustrious-v2",
      op: "edit",
      prompt: "1girl",
      negativePrompt: "blurry",
      size: "1216x832",
      params: { steps: 28, cfg: 5.5, sampler: "euler" },
      seed_policy: "fixed",
      seed: 42,
      loras: [{ name: "add-detail", weight: 0.8 }],
      jobs: 40,
      count: 2,
      inputs: ["generated/console/a.png"],
      mask: "m.png",
      strength: 0.4,
      out_dir: "out",
      label: "sweep",
      full_steps: true,
    };
    expect(studioFromForm(formFromStudio(d))).toEqual(d);
  });

  it("触っていないフォームは空の下書き（既定値を書かない）", () => {
    expect(studioFromForm(emptyDraft())).toEqual({});
  });

  it("打ちかけの数は値にしない", () => {
    expect(studioFromForm({ ...emptyDraft(), steps: "abc", cfg: "7." }).params).toEqual({ cfg: 7 });
  });

  it("差分の patch: 変えた鍵は新しい値、消した鍵は null、同じなら null", () => {
    const base: StudioDraft = { prompt: "a", params: { cfg: 7 }, label: "x" };
    expect(draftPatch(base, { prompt: "b", params: { cfg: 7 } })).toEqual({ prompt: "b", label: null });
    expect(draftPatch(base, { ...base })).toBeNull();
    // suggest_model is the agent's proposal, never sent back by the form.
    expect(draftPatch({ suggest_model: "x" }, {})).toBeNull();
  });

  // RFC 7386 は params を 1 段深く merge する: 消した摘みは null で名指さないと残る（レビュー 🔴C1）。
  it("params の摘みを 1 つ消すと、その摘みを null で送る", () => {
    const base: StudioDraft = { params: { steps: 30, cfg: 7, sampler: "euler" } };
    expect(draftPatch(base, { params: { steps: 30, cfg: 7 } })).toEqual({ params: { steps: 30, cfg: 7, sampler: null } });
    expect(draftPatch(base, {})).toEqual({ params: { steps: null, cfg: null, sampler: null } });
  });

  it("フォームが既定と区別できない値は null で送り返さない（人の編集を捏造しない）", () => {
    expect(draftPatch({ op: "generate", strength: 0.6, prompt: "a" }, { prompt: "a" })).toBeNull();
  });

  it("412 の後: 新しいスタジオの上に人が変えていた鍵だけ載せ直す", () => {
    const form = { ...emptyDraft(), prompt: "mine", cfg: "5", label: "old" };
    const out = rebaseForm(form, ["prompt"], { prompt: "agent", params: { cfg: 9 }, label: "new" });
    expect(out.prompt).toBe("mine");
    expect(out.cfg).toBe("9");
    expect(out.label).toBe("new");
  });

  it("鍵の順序は差分にしない", () => {
    expect(changedKeys({ params: { cfg: 7, steps: 20 } }, { params: { steps: 20, cfg: 7 } })).toEqual([]);
  });

  it("サーバが動いた鍵だけ差し替え、同じ意味の打ちかけは残す", () => {
    const form = { ...emptyDraft(), prompt: "mine", cfg: "7.", steps: "20" };
    const merged = mergeForm(form, { prompt: "theirs", params: { cfg: 7, steps: 20 } });
    expect(merged.prompt).toBe("theirs");
    expect(merged.cfg).toBe("7.");
    const moved = mergeForm(form, { prompt: "mine", params: { cfg: 5, steps: 20 } });
    expect(moved.cfg).toBe("5");
    expect(moved.prompt).toBe("mine");
  });

  it("何も変わらなければ同じオブジェクト（再描画しない）", () => {
    const form = { ...emptyDraft(), prompt: "a" };
    expect(mergeForm(form, { prompt: "a" })).toBe(form);
  });
});

describe("錠", () => {
  it("エージェントの欄だけ錠を掛けられる", () => {
    expect(isLockable("prompt")).toBe(true);
    expect(isLockable("params")).toBe(true);
    for (const k of ["model", "seed", "mask", "jobs", "suggest_model"]) expect(isLockable(k)).toBe(false);
  });

  it("掛け外しは往復する", () => {
    const on = toggleLock(["prompt"], "params");
    expect(on).toEqual(["params", "prompt"]);
    expect(toggleLock(on, "params")).toEqual(["prompt"]);
    expect(toggleLock(undefined, "size")).toEqual(["size"]);
  });
});

const log: DraftLogEntry[] = [
  { seq: 1, kind: "edit", at: "t1", author: "human", changes: [{ field: "prompt", before: "", after: "a" }], draft: { prompt: "a" } },
  { seq: 2, kind: "edit", at: "t2", author: "agent", changes: [{ field: "params.cfg", before: 7, after: 5 }, { field: "prompt", before: "a", after: "b" }], draft: { prompt: "b" } },
  { seq: 3, kind: "press", at: "t3", author: "human", version: "v-1", mode: "trial", draft: { prompt: "b" } },
  { seq: 4, kind: "press_result", at: "t3", version: "v-1", jobs: ["j1"], state: "ok" },
  { seq: 5, kind: "press_result", at: "t9", version: "v-1", state: "lost" },
  { seq: 6, kind: "press", at: "t4", author: "human", version: "v-2", mode: "enqueue", draft: { prompt: "b" } },
  { seq: 7, kind: "rewind", at: "t5", author: "rewind", rewind_to: 1, draft: { prompt: "a" } },
];

describe("編集履歴", () => {
  it("エージェントが動かした鍵（params.cfg は params）", () => {
    expect(agentTouched(log, 0)).toEqual(["params", "prompt"]);
    expect(agentTouched(log, 2)).toEqual([]);
  });

  it("版は新しい順、press_result は最初の 1 件だけを採る、結果の無い版は pending", () => {
    const v = versionsOf(log);
    expect(v.map((x) => x.version)).toEqual(["v-2", "v-1"]);
    expect(v[1].state).toBe("ok");
    expect(v[1].jobs).toEqual(["j1"]);
    expect(v[0].state).toBe("pending");
  });

  it("絵の版から戻す先の press 行を引く", () => {
    expect(pressSeqOf(log, "v-2")).toBe(6);
    expect(pressSeqOf(log, "v-9")).toBeNull();
    expect(pressSeqOf(log, undefined)).toBeNull();
  });

  it("変更の 1 行: params は 1 段開く", () => {
    expect(describeChange({ field: "params", before: { cfg: 7, steps: 20 }, after: { cfg: 5, steps: 20 } })).toEqual(["cfg 7→5"]);
    expect(describeChange({ field: "params.cfg", before: 7, after: 5 })).toEqual(["cfg 7→5"]);
    expect(describeChange({ field: "prompt", before: "", after: "x" })).toEqual(["prompt ∅→x"]);
  });
});

describe("合図 1 行（決定 5）", () => {
  it("since の畳み: 人の編集・新しい結果・巻き戻し", () => {
    expect(foldSince(log, 0)).toEqual({ seq: 7, draftChanged: true, newResults: 1, rewindTo: 1 });
    // After seq 4 the only press_result that counts (the first for v-1) is already seen.
    expect(foldSince(log, 4)).toEqual({ seq: 7, draftChanged: false, newResults: 0, rewindTo: 1 });
    expect(foldSince(log, 7).rewindTo).toBeUndefined();
  });

  it("[studio で始まり → get_image_studio] で終わる最終行で、転写の剥がし手が落とす", () => {
    const sig = studioSignal({ seq: 12, draftChanged: true, newResults: 2 }, words);
    expect(sig).toBe("[studio v12 · draft changed · 2 new results → get_image_studio]");
    const sent = withStudioSignal("もっと暗く\n", sig);
    expect(sent.split("\n").pop()).toBe(sig);
    expect(stripStudioSignal(sent)).toBe("もっと暗く");
  });
});

describe("エージェントを付ける: kind の候補（決定 8）", () => {
  it("TUI は terminalDriver !== false から shell を除く（claude と agy が残る）", () => {
    const tui = studioKindChoices("tui").map((c) => c.kind);
    expect(tui).toContain("claude");
    expect(tui).toContain("agy");
    expect(tui).not.toContain("lcpp");
    expect(tui).not.toContain("muse");
    expect(tui).not.toContain("shell");
  });

  it("Managed は managedDriver。opencode と muse と af 子が名前を知らない 3 つは理由付きで無効", () => {
    const m = studioKindChoices("managed");
    expect(m.map((c) => c.kind)).not.toContain("claude");
    const by = Object.fromEntries(m.map((c) => [c.kind, c.blocked ?? "ok"]));
    expect(by.codex).toBe("ok");
    expect(by.lcpp).toBe("ok");
    expect(by.opencode).toBe("af_shared");
    expect(by.muse).toBe("af_unreachable");
    expect(by.copilot).toBe("af_session_name");
  });

  it("接続で絞れる", () => {
    expect(studioKindChoices("tui", ["codex"]).map((c) => c.kind)).toEqual(["codex"]);
  });

  it("worktree を外せるのは Terminal と lcpp だけ", () => {
    expect(worktreeOptional("claude", "tui")).toBe(true);
    expect(worktreeOptional("lcpp", "managed")).toBe(true);
    expect(worktreeOptional("codex", "managed")).toBe(false);
  });
});
