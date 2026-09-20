// The queue's arithmetic (ADR 0081 decisions 8, 10, 11, 12).
//
// Every assertion here is a claim the SCREEN makes that a person acts on: how far a batch
// is, how long is left, whether the engine is awake, and what a trial actually submits. A
// wrong answer in any of them looks perfectly plausible, which is why they are pure
// functions with a test rather than expressions inside JSX.
import { describe, expect, it } from "vitest";
import {
  RUNNING_CAP,
  anyLive,
  barSegments,
  buildRequest,
  engineState,
  etaBucket,
  etaMs,
  foldGroups,
  seedsFor,
} from "./jobs.ts";
import { emptyDraft } from "./draft.ts";
import type { ImagegenStatus, Job, JobGroup } from "./wire.ts";

const job = (p: Partial<Job> & { id: string }): Job => ({ state: "queued", ...p });

describe("グループの畳み込み", () => {
  const jobs: Job[] = [
    job({ id: "1", group: "g1", state: "done" }),
    job({ id: "2", group: "g1", state: "running", started_at: "2026-09-13T00:00:00Z", typical_ms: 20_000 }),
    job({ id: "3", group: "g1", state: "queued", position: 1 }),
    job({ id: "t", trial: true, state: "done" }),
  ];
  const groups: JobGroup[] = [{ id: "g1", label: "sweep", state: "running", done: 1, failed: 0, total: 40 }];

  it("グループは 1 行、グループ無しのジョブは自分だけの行になる", () => {
    const rows = foldGroups(jobs, groups);
    expect(rows.map((r) => r.key)).toEqual(["g1", "job:t"]);
    expect(rows[0].jobs).toHaveLength(3);
    expect(rows[1].trial).toBe(true);
  });

  it("Agent の総数が勝つ（完了ジョブが一覧から落ちても 40 のまま）", () => {
    const rows = foldGroups(jobs, groups);
    expect(rows[0].total).toBe(40);
    expect(rows[0].done).toBe(1);
  });

  it("groups に無いグループ id でも行は消えない", () => {
    const rows = foldGroups(jobs, []);
    expect(rows[0].key).toBe("g1");
    expect(rows[0].total).toBe(3);
  });
});

describe("進捗バー", () => {
  const row = (over: Partial<ReturnType<typeof foldGroups>[number]> = {}) => ({
    key: "g",
    group: null,
    jobs: [],
    running: null,
    done: 0,
    failed: 0,
    cancelled: 0,
    total: 10,
    label: "",
    trial: false,
    ...over,
  });

  it("done と failed は件数どおり", () => {
    const s = barSegments(row({ done: 3, failed: 1 }), 0);
    expect(s.done).toBeCloseTo(0.3);
    expect(s.failed).toBeCloseTo(0.1);
  });

  it("実行中の区間は時間で満ち、95% で止まる", () => {
    const running = job({ id: "r", state: "running", started_at: "2026-09-13T00:00:00Z", typical_ms: 10_000 });
    const t0 = Date.parse("2026-09-13T00:00:00Z");
    const half = barSegments(row({ running }), t0 + 5_000);
    expect(half.running).toBeCloseTo(0.5 / 10);
    // Twice the estimate must NOT report the job as finished: the bar may never claim a
    // completion the Agent has not sent.
    const over = barSegments(row({ running }), t0 + 60_000);
    expect(over.running).toBeCloseTo(RUNNING_CAP / 10);
  });

  it("見積もりが無いジョブは区間を伸ばさない", () => {
    const running = job({ id: "r", state: "running", started_at: "2026-09-13T00:00:00Z" });
    expect(barSegments(row({ running }), Date.now()).running).toBe(0);
  });

  it("三つの区間は合計 1 を超えない", () => {
    const running = job({ id: "r", state: "running", started_at: "2026-09-13T00:00:00Z", typical_ms: 1 });
    const s = barSegments(row({ done: 9, failed: 1, running }), Date.now());
    expect(s.done + s.failed + s.running).toBeLessThanOrEqual(1);
  });
});

describe("残り時間", () => {
  const base = {
    key: "g",
    group: null,
    jobs: [],
    running: null,
    done: 0,
    failed: 0,
    cancelled: 0,
    total: 10,
    label: "",
    trial: false,
  };

  it("Agent の eta_ms が優先される", () => {
    const g: JobGroup = { id: "g", state: "running", done: 0, failed: 0, total: 10, eta_ms: 1234 };
    expect(etaMs({ ...base, group: g })).toBe(1234);
  });

  it("無ければ 残件 × typical_ms", () => {
    const running = job({ id: "r", state: "running", typical_ms: 30_000 });
    expect(etaMs({ ...base, running, done: 2 })).toBe(8 * 30_000);
  });

  it("どちらも無ければ null（数字を作らない）", () => {
    expect(etaMs(base)).toBeNull();
  });

  it("刻みは分・秒・まもなく", () => {
    expect(etaBucket(null)).toEqual({ unit: "soon", value: 0 });
    expect(etaBucket(5_000).unit).toBe("soon");
    expect(etaBucket(30_000)).toEqual({ unit: "sec", value: 30 });
    expect(etaBucket(9 * 60_000)).toEqual({ unit: "min", value: 9 });
  });
});

describe("seed の方針", () => {
  it("random は毎回 Agent が引く（Console は決めない）", () => {
    expect(seedsFor("random", 7, 3)).toEqual([null, null, null]);
  });
  it("fixed は同じ、sequence は base + i", () => {
    expect(seedsFor("fixed", 7, 3)).toEqual([7, 7, 7]);
    expect(seedsFor("sequence", 7, 3)).toEqual([7, 8, 9]);
  });
  it("base が無ければ fixed でも引けない", () => {
    expect(seedsFor("fixed", null, 2)).toEqual([null, null]);
  });
});

describe("エンジンの状態", () => {
  // The id is a real deployment's images ROW KEY (ADR 0082 P0), not the kind name "comfy": a
  // fixture spelled "comfy" cannot catch a reader that matches the id's spelling instead of
  // reading `fleet`, because "comfy" happens to satisfy both.
  const ready: ImagegenStatus = { enabled: true, ready: true, providers: [{ id: "image", fleet: true, models: [] }] };

  it("waking のジョブがあれば starting", () => {
    expect(engineState(ready, [job({ id: "1", state: "waking" })], { id: "m", warm: true })).toBe("starting");
  });
  it("選んだモデルが warm なら ready、そうでなければ cold", () => {
    expect(engineState(ready, [], { id: "m", warm: true })).toBe("ready");
    expect(engineState(ready, [], { id: "m" })).toBe("cold");
  });
  it("fleet の provider が無ければ unavailable", () => {
    expect(engineState({ enabled: true, ready: true, providers: [{ id: "agy" }] }, [], null)).toBe("unavailable");
    expect(engineState(null, [], null)).toBe("unavailable");
  });
});

describe("投入する本文", () => {
  const d = {
    ...emptyDraft(),
    prompt: "a cat",
    model: "sdxl",
    jobs: 40,
    batchSize: 2,
    steps: "28",
    seedPolicy: "fixed" as const,
    seed: "99",
    size: "1024x1024",
  };

  it("通常は N ジョブ・batch_size そのまま・trial 無し", () => {
    const b = buildRequest(d, { provider: "comfy" });
    expect(b).toMatchObject({ jobs: 40, count: 2, seed_policy: "fixed", seed: 99, provider: "comfy" });
    expect(b.trial).toBeUndefined();
    expect(b.params).toEqual({ steps: 28 });
  });

  it("試走は 1 枚・batch 1・trial:true。steps は params に残す", () => {
    const b = buildRequest(d, { trial: true });
    expect(b.jobs).toBe(1);
    expect(b.count).toBe(1);
    expect(b.trial).toBe(true);
    // The form's steps ride along so the sidecar records what the BATCH would run; the
    // Agent substitutes the family's trial value for the run itself.
    expect(b.params).toEqual({ steps: 28 });
    expect(b.size).toBe("1024x1024");
  });

  it("試走の「steps を減らさない」は full_steps で伝える（本番には付けない）", () => {
    expect(buildRequest({ ...d, fullSteps: true }, { trial: true }).full_steps).toBe(true);
    expect(buildRequest({ ...d, fullSteps: true }).full_steps).toBeUndefined();
  });

  it("ネガティブは既存の綴り negativePrompt で送る（MCP と同じ経路の鍵）", () => {
    expect(buildRequest({ ...d, negative: "blurry" }).negativePrompt).toBe("blurry");
  });

  it("random のときは seed を送らない", () => {
    expect(buildRequest({ ...d, seedPolicy: "random" }).seed).toBeUndefined();
  });

  it("generate では inputs と strength を送らない", () => {
    const b = buildRequest({ ...d, inputs: ["a.png"], strength: 0.3 });
    expect(b.inputs).toBeUndefined();
    expect(b.strength).toBeUndefined();
    const e = buildRequest({ ...d, op: "edit", inputs: ["a.png"], strength: 0.3 });
    expect(e).toMatchObject({ op: "edit", inputs: ["a.png"], strength: 0.3 });
  });

  // ADR 0094 決定 2: strength を読まない族には送らない——読む・読まないの答えは Agent の
  // knobs だけが持つ。knobs が無い（古い Agent）ときは今までどおり送る。
  it("モデルの knobs に strength が無ければ送らない", () => {
    const withKnob = buildRequest(
      { ...d, op: "edit", inputs: ["a.png"], strength: 0.3 },
      { model: { id: "sdxl-base", knobs: ["steps", "cfg", "sampler", "scheduler", "negative", "strength"] } },
    );
    expect(withKnob.strength).toBe(0.3);

    const withoutKnob = buildRequest(
      { ...d, op: "edit", inputs: ["a.png"], strength: 0.3 },
      { model: { id: "qwen-edit-row", knobs: ["steps", "cfg", "sampler", "scheduler", "negative"] } },
    );
    expect(withoutKnob.strength).toBeUndefined();

    // No model at all (or one with no `knobs`, an Agent old enough to predate the ADR): the
    // old, permissive behaviour — send it, and let the Agent's own warning or refusal answer.
    const noModel = buildRequest({ ...d, op: "edit", inputs: ["a.png"], strength: 0.3 }, { model: null });
    expect(noModel.strength).toBe(0.3);
    const oldAgent = buildRequest(
      { ...d, op: "edit", inputs: ["a.png"], strength: 0.3 },
      { model: { id: "legacy" } },
    );
    expect(oldAgent.strength).toBe(0.3);
  });

  // 🔴 ADR 0094 決定 4: draft.size は永続化される（別モデルへ切り替えても残る）ので、
  // モデルが sizes を持たない族に切り替わった後もそのまま送ると毎回 400 になる。
  // GenerateForm の大きさ欄は sizes が空だと消えるので、値を消す手段は buildRequest 側にしか無い。
  it("モデルが sizes を持たなければ、残っていた draft.size を送らない", () => {
    const stuck = buildRequest(
      { ...d, op: "edit", size: "1024x1024" },
      { model: { id: "qwen-edit-row", family: "qwen-image-edit-2509", sizes: [] } },
    );
    expect(stuck.size).toBeUndefined();

    // 陽性対照: sizes を持つモデルでは今までどおり送る。
    const ok = buildRequest(
      { ...d, op: "edit", size: "1024x1024" },
      { model: { id: "sdxl-base", family: "sdxl" } },
    );
    expect(ok.size).toBe("1024x1024");
  });
});

describe("走っているものがあるか（ポーリングの唯一の条件）", () => {
  it("未完了が 1 つでもあれば true、全部終わっていれば false", () => {
    expect(anyLive([job({ id: "1", state: "done" }), job({ id: "2", state: "queued" })])).toBe(true);
    expect(anyLive([job({ id: "1", state: "done" }), job({ id: "2", state: "cancelled" })])).toBe(false);
    expect(anyLive([])).toBe(false);
    expect(anyLive(undefined)).toBe(false);
  });
});
