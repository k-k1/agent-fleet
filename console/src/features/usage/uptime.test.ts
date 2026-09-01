// 稼働時間ヒートマップの純関数（docs/log/83）。
//
// ⚠️ タイムゾーンをこのファイルで固定する。既定のままだと CI と開発機で違う結果になり、
// 「+09:00 で日付がずれる」という**この機能の要点そのもの**が検査できない。
// Node は process.env.TZ の変更を後から反映する（実測）。
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  buildGrid,
  cellLevel,
  cellValue,
  dayRange,
  isUnmeasured,
  levelBand,
  localBucket,
  maxValue,
  minutesOf,
  widenForTimezone,
} from "./uptime.ts";
import type { UptimeResponse } from "./uptime.ts";

const ORIGINAL_TZ = process.env.TZ;
beforeAll(() => {
  process.env.TZ = "Asia/Tokyo";
});
afterAll(() => {
  process.env.TZ = ORIGINAL_TZ;
});

describe("UTC → ローカルの畳み込み", () => {
  // ⚠️ ここがずれると、日本の利用者のヒートマップは 9 時間ずれて
  // 「毎晩深夜に働いている」という表になる。
  it("UTC の時バケットを閲覧者のローカル時刻へ運ぶ", () => {
    expect(localBucket("2026-09-01T00")).toEqual({ day: "2026-09-01", hour: 9 });
    // 日付をまたぐ側。UTC の 15:00 はローカルの翌日 0 時。
    expect(localBucket("2026-09-01T15")).toEqual({ day: "2026-09-02", hour: 0 });
  });

  it("壊れたキーは落とす（例外にしない）", () => {
    expect(localBucket("")).toBeNull();
    expect(localBucket("2026-09-01")).toBeNull();
  });

  // 日付をまたぐぶんを取り寄せないと、端の列が半分空く。
  it("要求する窓は前後 1 日ぶん広げる", () => {
    expect(widenForTimezone("2026-09-01", "2026-09-10")).toEqual({
      from: "2026-08-31",
      to: "2026-09-11",
    });
  });

  it("日付の列は両端を含む", () => {
    expect(dayRange("2026-08-30", "2026-09-02")).toEqual([
      "2026-08-30",
      "2026-08-31",
      "2026-09-01",
      "2026-09-02",
    ]);
  });
});

const RES: UptimeResponse = {
  from: "2026-09-01",
  to: "2026-09-01",
  interval_secs: 300,
  // UTC 00 時 = ローカル 9 時。samples はマスの分母（見ていた秒数）。
  observed: [
    { hour: "2026-09-01T00", samples: 12 },
    { hour: "2026-09-01T01", samples: 12 },
  ],
  members: [
    {
      tenant: "sales",
      user_key: "aoi",
      email: "aoi@example.com",
      hours: [
        {
          hour: "2026-09-01T00",
          samples: 12,
          running_secs: 3600,
          measured_secs: 3600,
          session_secs: 7200,
          busy_secs: 1800,
          max_sessions: 3,
          max_busy: 1,
        },
      ],
    },
    {
      tenant: "sales",
      user_key: "bun",
      email: "bun@example.com",
      hours: [
        {
          hour: "2026-09-01T00",
          samples: 12,
          running_secs: 1800,
          measured_secs: 1800,
          session_secs: 1800,
          busy_secs: 0,
          max_sessions: 1,
          max_busy: 0,
        },
        // 起きてはいたが Agent に届かなかった時間（measured なし）。
        { hour: "2026-09-01T01", samples: 12, running_secs: 3600, max_sessions: 0 },
      ],
    },
  ],
};

describe("マス目の組み立て", () => {
  const grid = buildGrid(RES);

  it("メンバーの寄与を 1 マスに積む", () => {
    const c = grid.get("2026-09-01|9")!;
    expect(c.runningSecs).toBe(5400);
    expect(c.sessionSecs).toBe(9000);
    // ⚠️ 分母はハートビート 1 本ぶん（＝その時間）。メンバーの samples を足すと、
    // 合算のマスが「平均何台」ではなく「動いていた割合」になってしまう。
    expect(c.possibleSecs).toBe(12 * 300);
    // ピークは足さない。合計 4 ではなく、同時に見えた最大の 3。
    expect(c.maxSessions).toBe(3);
  });

  // ⚠️ observed はハートビートだけから作る。メンバーの行から推測すると、CP が
  // 落ちていた時間と誰も働かなかった時間が同じ灰色になる。
  it("観測できた時間はハートビート由来で、メンバーの行とは独立", () => {
    expect(grid.get("2026-09-01|9")!.observed).toBe(true);
    expect(grid.get("2026-09-01|10")!.observed).toBe(true);
    // ハートビートの無い時間は行そのものが無い＝空白。
    expect(grid.get("2026-09-01|11")).toBeUndefined();
  });

  it("内訳は寄与の多い順（ホバーで上から 3 件出すので並びが情報になる）", () => {
    expect(grid.get("2026-09-01|9")!.contributions.map((c) => c.label)).toEqual(["aoi", "bun"]);
  });

  it("平均本数の分母は running ではなく measured", () => {
    // 稼働 5400 秒・セッション秒 9000 → 平均 1.67 本。
    expect(cellValue(grid.get("2026-09-01|9"), "sessions")).toBeCloseTo(9000 / 5400, 5);
    expect(cellValue(grid.get("2026-09-01|9"), "busy")).toBeCloseTo(1800 / 5400, 5);
  });

  // ⚠️ 分母が「その時間 1 つぶん」だから、同じ式が 1 人なら稼働率・合算なら台数になる。
  // メンバーの samples を足した分母にすると、合算が「動いていた割合」に化ける。
  it("稼働率は「その時間に平均で何台」（1 人なら 0..1）", () => {
    // 5400 秒 / 3600 秒（12 サンプル × 300 秒）= 平均 1.5 台
    expect(cellValue(grid.get("2026-09-01|9"), "running")).toBeCloseTo(1.5, 5);
  });

  // ⚠️ ハートビートを取り損ねた時間で 0 除算しない（Infinity がそのまま段の計算に入る）。
  it("ハートビートが無くても稼働している時間は分母を持つ", () => {
    const g = buildGrid({
      ...RES,
      observed: [],
      members: [
        {
          tenant: "sales",
          user_key: "aoi",
          email: "",
          hours: [{ hour: "2026-09-01T00", samples: 12, running_secs: 1800 }],
        },
      ],
    });
    expect(cellValue(g.get("2026-09-01|9"), "running")).toBeCloseTo(0.5, 5);
  });

  // ⚠️ 届かなかった Agent を「0 本」と描くと、忙しかった時間に冷たいマスが出る。
  it("本数不明の時間を 0 本と同じにしない", () => {
    const c = grid.get("2026-09-01|10")!;
    expect(c.runningSecs).toBe(3600);
    expect(isUnmeasured(c, "sessions")).toBe(true);
    // 稼働率の指標では「不明」は無い（走っていた秒数は分かっている）。
    expect(isUnmeasured(c, "running")).toBe(false);
  });

  it("最大値は指標ごとに取り直す", () => {
    const days = ["2026-09-01"];
    expect(maxValue(grid, days, "sessions")).toBeCloseTo(9000 / 5400, 5);
    expect(maxValue(grid, days, "busy")).toBeCloseTo(1800 / 5400, 5);
  });

  it("null や空の応答で落ちない", () => {
    expect(buildGrid(null).size).toBe(0);
    expect(buildGrid({ ...RES, observed: [], members: [] }).size).toBe(0);
  });
});

describe("色の段", () => {
  // ⚠️ 本数の指標では「起きていたが 0 本」が最下段。灰色（止まっていた）とは別物なので、
  // 段を 0 にして描かない、という扱いはできない。
  it("本数の指標では 0 が最下段", () => {
    expect(cellLevel(0, 4, true)).toBe(1);
    expect(cellLevel(0.1, 4, true)).toBe(2);
    expect(cellLevel(4, 4, true)).toBe(5);
  });

  it("稼働率の指標は 5 段を全部使う", () => {
    expect(cellLevel(0.01, 1, false)).toBe(1);
    expect(cellLevel(1, 1, false)).toBe(5);
  });

  it("最大が 0 でも段は 1 に落ちる（0 除算で NaN の class を作らない）", () => {
    expect(cellLevel(0, 0, true)).toBe(1);
    expect(cellLevel(0, 0, false)).toBe(1);
  });

  it("凡例の帯は段と同じ分け方", () => {
    expect(levelBand(1, 4, true)).toEqual([0, 0]);
    expect(levelBand(2, 4, true)).toEqual([0, 1]);
    expect(levelBand(5, 4, true)).toEqual([3, 4]);
    expect(levelBand(1, 1, false)).toEqual([0, 0.2]);
  });
});

describe("分の表示", () => {
  // 30 秒を「0 分」と出すと「動いていない」と読まれる。
  it("0 でない稼働は必ず 1 分以上", () => {
    expect(minutesOf(0)).toBe(0);
    expect(minutesOf(20)).toBe(1);
    expect(minutesOf(3600)).toBe(60);
  });
});
