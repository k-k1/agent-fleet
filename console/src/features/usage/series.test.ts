import { describe, it, expect } from "vitest";
import type { UsageAgg, UsageBucket } from "./api.ts";
import {
  addAgg,
  bucketFor,
  breakdownRows,
  coverageNotes,
  isMoneyMetric,
  matrixRows,
  filterParam,
  foldedFilterOn,
  metricOf,
  perCall,
  rangeOf,
  stackModel,
  toggleFoldedFilter,
  totalsByKey,
} from "./series.ts";
import { OTHER_KEY } from "./colors.ts";

const agg = (spend: number, calls = 1, extra: Partial<UsageAgg> = {}): UsageAgg => ({
  spend,
  in: spend,
  out: 0,
  cread: 0,
  ccreate: 0,
  calls,
  ...extra,
});

const buckets: UsageBucket[] = [
  {
    t: "2026-07-25T00:00:00Z",
    series: { session: agg(1000, 10), compact: agg(300, 2), "title.session": agg(50, 5) },
  },
  { t: "2026-07-26T00:00:00Z", series: { session: agg(2000, 20), compact: agg(100, 1) } },
];

describe("totalsByKey", () => {
  it("バケットを跨いで系列ごとに足す", () => {
    const m = totalsByKey(buckets);
    expect(m.get("session")?.spend).toBe(3000);
    expect(m.get("session")?.calls).toBe(30);
    expect(m.get("title.session")?.spend).toBe(50);
  });
});

describe("stackModel", () => {
  it("バケット合計と縦軸の上端を出す", () => {
    const s = stackModel(buckets, "feature", "spend");
    expect(s.rows.map((r) => r.total)).toEqual([1350, 2100]);
    expect(s.max).toBe(2100);
  });

  it("セグメントは必ずスロット順（＝隣接ペアが検証済みの並びになる）", () => {
    const s = stackModel(buckets, "feature", "spend");
    // session(1) → compact(4) → title.session(5)
    expect(s.rows[0].segs.map((x) => x.key)).toEqual(["session", "compact", "title.session"]);
  });

  it("凡例はスロット順、値0の系列はそのバケットに出さない", () => {
    const s = stackModel(buckets, "feature", "spend");
    expect(s.legend.map((l) => l.key)).toEqual(["session", "compact", "title.session"]);
    expect(s.rows[1].segs.map((x) => x.key)).toEqual(["session", "compact"]);
  });

  it("8系列を超えたら「その他」へ畳み、合計は保存される", () => {
    const many: UsageBucket = { t: "2026-07-26T00:00:00Z", series: {} };
    let sum = 0;
    for (let i = 0; i < 14; i++) {
      many.series[`model-${i}`] = agg(100 + i);
      sum += 100 + i;
    }
    const s = stackModel([many], "model", "spend");
    expect(s.legend.some((l) => l.key === OTHER_KEY)).toBe(true);
    expect(s.legend.length).toBe(9); // 8スロット + その他
    expect(s.rows[0].total).toBe(sum); // 畳んでも総量は失われない
    expect(s.foldedKeys.length).toBe(6);
  });

  it("指標を切り替えると回数でも積める", () => {
    const s = stackModel(buckets, "feature", "calls");
    expect(s.rows[0].total).toBe(17);
    expect(s.max).toBe(21);
  });

  it("空データでも壊れない", () => {
    const s = stackModel([], "feature", "spend");
    expect(s.rows).toEqual([]);
    expect(s.max).toBe(0);
    expect(s.legend).toEqual([]);
  });
});

describe("breakdownRows", () => {
  it("量の多い順・最大に対する比・全体シェア", () => {
    const rows = breakdownRows(totalsByKey(buckets), "feature", "spend");
    expect(rows.map((r) => r.key)).toEqual(["session", "compact", "title.session"]);
    expect(rows[0].frac).toBe(1);
    expect(rows[1].frac).toBeCloseTo(400 / 3000, 5);
    expect(rows[0].share).toBeCloseTo(3000 / 3450, 5);
  });

  it("色は積み上げ棒と同じ実体固定の色（読み手が2つのグラフを行き来できる）", () => {
    const rows = breakdownRows(totalsByKey(buckets), "feature", "spend");
    const stack = stackModel(buckets, "feature", "spend");
    const bySession = stack.legend.find((l) => l.key === "session");
    expect(rows.find((r) => r.key === "session")?.color).toBe(bySession?.color);
  });
});

describe("matrixRows", () => {
  it("行は spend 降順、行内のセルも降順、行合計を持つ", () => {
    const rows = matrixRows({
      "title.session": { "claude-haiku-4-5": agg(16, 4) },
      session: { "claude-opus-4-8": agg(11_464_050, 461), "claude-fable-5": agg(5_056_597, 201) },
    });
    expect(rows.map((r) => r.key)).toEqual(["session", "title.session"]);
    expect(rows[0].cells.map((c) => c.key)).toEqual(["claude-opus-4-8", "claude-fable-5"]);
    expect(rows[0].total.spend).toBe(11_464_050 + 5_056_597);
    expect(rows[0].total.calls).toBe(662);
  });

  it("matrix 無しは空配列（split を指定しなかった応答）", () => {
    expect(matrixRows(undefined)).toEqual([]);
  });
});

describe("coverageNotes — 「0」と「未計測」を混同させない", () => {
  it("トークン完全＋モデル報告なら注記不要、それ以外は注記対象", () => {
    const notes = coverageNotes({
      claude: { tokens: "exact", model: "reported" },
      copilot: { tokens: "partial", model: "reported" },
      cursor: { tokens: "none", model: "none" },
    });
    expect(notes.find((n) => n.kind === "claude")?.complete).toBe(true);
    expect(notes.find((n) => n.kind === "copilot")?.complete).toBe(false);
    expect(notes.find((n) => n.kind === "cursor")?.complete).toBe(false);
  });

  it("欠けたフィールドは none 扱い（黙って「測れている」に倒さない）", () => {
    const notes = coverageNotes({ agy: {} as never });
    expect(notes[0]).toMatchObject({ kind: "agy", tokens: "none", model: "none", complete: false });
  });
});

describe("指標・期間", () => {
  it("metricOf はコストの欠損を 0 として扱う", () => {
    expect(metricOf(agg(10), "spend")).toBe(10);
    expect(metricOf(agg(10), "cost_usd")).toBe(0);
    expect(metricOf(agg(10, 1, { cost_usd: 0.02 }), "cost_usd")).toBe(0.02);
    expect(metricOf(undefined, "spend")).toBe(0);
  });

  // 推定額と実測は**別の値**。片方をもう片方の欠損補完に使わない（1つの数字に2つの
  // 計測法が混ざると、どちらとしても読めなくなる）。
  it("推定額は実測とは独立に読み書きされる", () => {
    expect(metricOf(agg(10, 1, { cost_est_usd: 1.5 }), "cost_est_usd")).toBe(1.5);
    expect(metricOf(agg(10, 1, { cost_est_usd: 1.5 }), "cost_usd")).toBe(0);
    expect(metricOf(agg(10, 1, { cost_usd: 0.02 }), "cost_est_usd")).toBe(0);
    expect(isMoneyMetric("cost_est_usd")).toBe(true);
    expect(isMoneyMetric("spend")).toBe(false);
    const sum = addAgg(agg(10, 1, { cost_est_usd: 1.5 }), agg(10, 1, { cost_est_usd: 2.25 }));
    expect(sum.cost_est_usd).toBe(3.75);
  });

  it("perCall は calls=0 でも壊れない", () => {
    expect(perCall(agg(100, 4))).toBe(25);
    expect(perCall(agg(100, 0))).toBe(0);
  });

  it("24時間以内は時間粒度、それ以上は日粒度", () => {
    expect(bucketFor(24)).toBe("hour");
    expect(bucketFor(24 * 7)).toBe("day");
  });

  it("rangeOf は now を基準に from/to を作る", () => {
    const now = new Date("2026-07-26T09:00:00Z");
    const r = rangeOf(24, now);
    expect(r.to).toBe("2026-07-26T09:00:00.000Z");
    expect(r.from).toBe("2026-07-25T09:00:00.000Z");
    expect(r.bucket).toBe("hour");
  });
});

// --- レビュー P2-6: 畳まれた系列（色スロットの無い feature）の扱い ------------------

describe("その他（畳み込み）の絞り込み", () => {
  // 凍結 enum 12 個に対して色スロットは 8 つ。溢れた feature は必ずグレーの「その他」に
  // 入るので、その他が実キーへ展開できないと**グレーの棒だけ中身を確かめられない**。
  it("expands OTHER into every folded key (same-axis OR)", () => {
    const folded = ["assistant.ask", "title.chat"];
    const on = toggleFoldedFilter([], "feature", folded);
    expect(on.map((f) => f.dim + ":" + f.value).sort()).toEqual(["feature:assistant.ask", "feature:title.chat"]);
    expect(filterParam(on)).toContain("feature:assistant.ask");
    expect(foldedFilterOn(on, "feature", folded)).toBe(true);
    // もう一度押すと全部外れる（他の軸の絞り込みは残す）。
    const withOther = [...on, { dim: "kind", value: "claude" }];
    const off = toggleFoldedFilter(withOther, "feature", folded);
    expect(off).toEqual([{ dim: "kind", value: "claude" }]);
    expect(foldedFilterOn(off, "feature", folded)).toBe(false);
  });

  it("treats a partially selected OTHER as off (pressing it selects all)", () => {
    const folded = ["assistant.ask", "title.chat"];
    const partial = [{ dim: "feature", value: "title.chat" }];
    expect(foldedFilterOn(partial, "feature", folded)).toBe(false);
    expect(toggleFoldedFilter(partial, "feature", folded)).toHaveLength(2);
  });

  it("does nothing when there is nothing folded", () => {
    const cur = [{ dim: "feature", value: "session" }];
    expect(toggleFoldedFilter(cur, "feature", [])).toBe(cur);
    expect(foldedFilterOn(cur, "feature", [])).toBe(false);
  });

  // 色スロットを持たない feature（assistant.ask / title.chat / branch.suggest /
  // suggest.chat）は必ず畳まれる＝凡例に「その他」が要る、を stackModel 側で固定する。
  it("folds the four slot-less features into OTHER", () => {
    const b: UsageBucket[] = [
      {
        t: "2026-07-26T00:00:00Z",
        series: {
          "assistant.ask": agg(10),
          "title.chat": agg(20),
          "branch.suggest": agg(30),
          "suggest.chat": agg(40),
        },
      },
    ];
    const m = stackModel(b, "feature", "spend");
    expect(m.foldedKeys.sort()).toEqual(["assistant.ask", "branch.suggest", "suggest.chat", "title.chat"]);
    expect(m.legend.map((l) => l.key)).toEqual([OTHER_KEY]);
    expect(m.rows[0].total).toBe(100);
  });
});

// P3-9: サーバが空バケットもゼロで返すようになった。位置を保ったまま「棒の無い日」として
// 描けること（合計 0・セグメント無し）＝時間軸が読める形。
describe("ゼロ埋めされたバケット", () => {
  it("keeps empty buckets as gaps instead of collapsing the axis", () => {
    const buckets: UsageBucket[] = [
      { t: "2026-07-24T00:00:00Z", series: { session: agg(100) } },
      { t: "2026-07-25T00:00:00Z", series: {} },
      { t: "2026-07-26T00:00:00Z", series: { session: agg(300) } },
    ];
    const m = stackModel(buckets, "feature", "spend");
    expect(m.rows.map((r) => r.t)).toEqual(buckets.map((b) => b.t));
    expect(m.rows[1].total).toBe(0);
    expect(m.rows[1].segs).toEqual([]);
    expect(m.max).toBe(300); // 空バケットは縦軸を動かさない
  });
});
