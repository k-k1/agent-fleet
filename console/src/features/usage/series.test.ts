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
  it("sums each series across buckets", () => {
    const m = totalsByKey(buckets);
    expect(m.get("session")?.spend).toBe(3000);
    expect(m.get("session")?.calls).toBe(30);
    expect(m.get("title.session")?.spend).toBe(50);
  });
});

describe("stackModel", () => {
  it("produces the bucket totals and the top of the y axis", () => {
    const s = stackModel(buckets, "feature", "spend");
    expect(s.rows.map((r) => r.total)).toEqual([1350, 2100]);
    expect(s.max).toBe(2100);
  });

  it("always orders segments by slot, so the adjacent pairs are the validated ones", () => {
    const s = stackModel(buckets, "feature", "spend");
    // session(1) → compact(4) → title.session(5)
    expect(s.rows[0].segs.map((x) => x.key)).toEqual(["session", "compact", "title.session"]);
  });

  it("orders the legend by slot and omits zero-valued series from a bucket", () => {
    const s = stackModel(buckets, "feature", "spend");
    expect(s.legend.map((l) => l.key)).toEqual(["session", "compact", "title.session"]);
    expect(s.rows[1].segs.map((x) => x.key)).toEqual(["session", "compact"]);
  });

  it("folds past 8 series into other while preserving the total", () => {
    const many: UsageBucket = { t: "2026-07-26T00:00:00Z", series: {} };
    let sum = 0;
    for (let i = 0; i < 14; i++) {
      many.series[`model-${i}`] = agg(100 + i);
      sum += 100 + i;
    }
    const s = stackModel([many], "model", "spend");
    expect(s.legend.some((l) => l.key === OTHER_KEY)).toBe(true);
    expect(s.legend.length).toBe(9); // 8 slots + other
    expect(s.rows[0].total).toBe(sum); // folding never loses magnitude
    expect(s.foldedKeys.length).toBe(6);
  });

  it("stacks by call count when the metric is switched", () => {
    const s = stackModel(buckets, "feature", "calls");
    expect(s.rows[0].total).toBe(17);
    expect(s.max).toBe(21);
  });

  it("survives empty data", () => {
    const s = stackModel([], "feature", "spend");
    expect(s.rows).toEqual([]);
    expect(s.max).toBe(0);
    expect(s.legend).toEqual([]);
  });
});

describe("breakdownRows", () => {
  it("orders by magnitude and reports the ratio to the largest and the overall share", () => {
    const rows = breakdownRows(totalsByKey(buckets), "feature", "spend");
    expect(rows.map((r) => r.key)).toEqual(["session", "compact", "title.session"]);
    expect(rows[0].frac).toBe(1);
    expect(rows[1].frac).toBeCloseTo(400 / 3000, 5);
    expect(rows[0].share).toBeCloseTo(3000 / 3450, 5);
  });

  it("reuses the entity-pinned colours of the stacked bars so a reader can move between charts", () => {
    const rows = breakdownRows(totalsByKey(buckets), "feature", "spend");
    const stack = stackModel(buckets, "feature", "spend");
    const bySession = stack.legend.find((l) => l.key === "session");
    expect(rows.find((r) => r.key === "session")?.color).toBe(bySession?.color);
  });
});

describe("matrixRows", () => {
  it("sorts rows and their cells by descending spend and carries a row total", () => {
    const rows = matrixRows({
      "title.session": { "claude-haiku-4-5": agg(16, 4) },
      session: { "claude-opus-4-8": agg(11_464_050, 461), "claude-fable-5": agg(5_056_597, 201) },
    });
    expect(rows.map((r) => r.key)).toEqual(["session", "title.session"]);
    expect(rows[0].cells.map((c) => c.key)).toEqual(["claude-opus-4-8", "claude-fable-5"]);
    expect(rows[0].total.spend).toBe(11_464_050 + 5_056_597);
    expect(rows[0].total.calls).toBe(662);
  });

  it("returns an empty array with no matrix, i.e. a response with no split", () => {
    expect(matrixRows(undefined)).toEqual([]);
  });
});

describe("coverageNotes: never confuse zero with unmeasured", () => {
  it("needs no note for exact tokens plus a reported model, and a note for anything else", () => {
    const notes = coverageNotes({
      claude: { tokens: "exact", model: "reported" },
      copilot: { tokens: "partial", model: "reported" },
      cursor: { tokens: "none", model: "none" },
    });
    expect(notes.find((n) => n.kind === "claude")?.complete).toBe(true);
    expect(notes.find((n) => n.kind === "copilot")?.complete).toBe(false);
    expect(notes.find((n) => n.kind === "cursor")?.complete).toBe(false);
  });

  it("treats a missing field as none rather than silently claiming it was measured", () => {
    const notes = coverageNotes({ agy: {} as never });
    expect(notes[0]).toMatchObject({ kind: "agy", tokens: "none", model: "none", complete: false });
  });
});

describe("metrics and ranges", () => {
  it("metricOf treats a missing cost as 0", () => {
    expect(metricOf(agg(10), "spend")).toBe(10);
    expect(metricOf(agg(10), "cost_usd")).toBe(0);
    expect(metricOf(agg(10, 1, { cost_usd: 0.02 }), "cost_usd")).toBe(0.02);
    expect(metricOf(undefined, "spend")).toBe(0);
  });

  // The estimate and the measured cost are different values; neither fills in for the other. A
  // number that mixes two ways of measuring can no longer be read as either.
  it("reads and writes the estimated amount independently of the measured one", () => {
    expect(metricOf(agg(10, 1, { cost_est_usd: 1.5 }), "cost_est_usd")).toBe(1.5);
    expect(metricOf(agg(10, 1, { cost_est_usd: 1.5 }), "cost_usd")).toBe(0);
    expect(metricOf(agg(10, 1, { cost_usd: 0.02 }), "cost_est_usd")).toBe(0);
    expect(isMoneyMetric("cost_est_usd")).toBe(true);
    expect(isMoneyMetric("spend")).toBe(false);
    const sum = addAgg(agg(10, 1, { cost_est_usd: 1.5 }), agg(10, 1, { cost_est_usd: 2.25 }));
    expect(sum.cost_est_usd).toBe(3.75);
  });

  it("perCall survives calls=0", () => {
    expect(perCall(agg(100, 4))).toBe(25);
    expect(perCall(agg(100, 0))).toBe(0);
  });

  it("uses hour grain up to 24 hours and day grain beyond", () => {
    expect(bucketFor(24)).toBe("hour");
    expect(bucketFor(24 * 7)).toBe("day");
  });

  it("rangeOf builds from/to relative to now", () => {
    const now = new Date("2026-07-26T09:00:00Z");
    const r = rangeOf(24, now);
    expect(r.to).toBe("2026-07-26T09:00:00.000Z");
    expect(r.from).toBe("2026-07-25T09:00:00.000Z");
    expect(r.bucket).toBe("hour");
  });
});

// --- How folded series (features with no colour slot) behave -----------------------

describe("filtering on other (the folded series)", () => {
  // 12 frozen enum values against 8 colour slots: the overflow always lands in the grey other,
  // so unless other expands to the real keys the grey bar is the one bar nobody can inspect.
  it("expands OTHER into every folded key (same-axis OR)", () => {
    const folded = ["assistant.ask", "title.chat"];
    const on = toggleFoldedFilter([], "feature", folded);
    expect(on.map((f) => f.dim + ":" + f.value).sort()).toEqual(["feature:assistant.ask", "feature:title.chat"]);
    expect(filterParam(on)).toContain("feature:assistant.ask");
    expect(foldedFilterOn(on, "feature", folded)).toBe(true);
    // Pressing again clears them all, keeping filters on other axes.
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

  // Pins in stackModel that the features with no colour slot (assistant.ask / title.chat /
  // branch.suggest / suggest.chat) always fold, which is why the legend needs an other entry.
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

// The server returns empty buckets as zeros, and they must keep their position and draw as a
// day with no bar (total 0, no segments); that is what keeps the time axis readable.
describe("zero-filled buckets", () => {
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
    expect(m.max).toBe(300); // an empty bucket does not move the y axis
  });
});
