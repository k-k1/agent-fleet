// 使用量ビューの描画。どちらの describe も「数字の中身」ではなく**数字の顔つき**を見る
// （前半＝鮮度、後半＝推定額）。
//
// 鮮度で押さえるのは **古い数字を最新の顔で見せないこと** の 2 点:
//   ① セッション本体の折り込み（fold-on-read）は非同期なので、走っている間の応答は直近
//      ターンを含まない。サーバが folding を立てて返している間は、こちらが自動で取り直す。
//      これが無いと「再取得を何度か押すまで最新にならない」画面になる（実際の苦情）。
//   ② 明示的な再取得は fold=force を送る。送らないと 60 秒スロットルに当たり、押した
//      時点で終わっているターンが最大1分ぶん入ってこない＝押しても何も変わらない。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { UsageQuery, UsageSeries } from "./api.ts";

const fetchUsageSeries = vi.fn();
vi.mock("./api.ts", () => ({
  fetchUsageSeries: (q: UsageQuery, s?: AbortSignal) => fetchUsageSeries(q, s),
  // rtk 効果カードは別系（この面の鮮度とは無関係）。不在扱いで黙って隠させる。
  fetchRtkGain: () => Promise.resolve({ available: false }),
}));
vi.mock("../../core/api/client.ts", () => ({
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));

const { UsageView } = await import("./UsageView.tsx");

const series = (spend: number, folding: boolean, extra: Partial<UsageSeries> = {}): UsageSeries => ({
  from: "2026-08-18T00:00:00Z",
  to: "2026-08-19T00:00:00Z",
  bucket: "day",
  by: "feature",
  buckets: [
    {
      t: "2026-08-18T00:00:00Z",
      series: { session: { spend, in: spend, out: 0, cread: 0, ccreate: 0, calls: 1 } },
    },
  ],
  totals: { spend, in: spend, out: 0, cread: 0, ccreate: 0, calls: 1 },
  coverage: {},
  unmeasured_calls: 0,
  folding,
  ...extra,
});

const kpiText = (label: string): string => {
  const tile = [...(host?.querySelectorAll(".ukpi") || [])].find((el) =>
    el.querySelector(".ukpi-lab")?.textContent?.includes(label),
  );
  return tile?.querySelector(".ukpi-val")?.textContent || "";
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const mount = async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<UsageView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
};

const folding = () => !!host?.querySelector(".uc-folding");
const queries = (): UsageQuery[] => fetchUsageSeries.mock.calls.map((c) => c[0] as UsageQuery);

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  fetchUsageSeries.mockReset();
  vi.useRealTimers();
});

describe("UsageView の鮮度", () => {
  it("折り込み中の応答は自動で取り直し、終わったら止める", async () => {
    // 1巡目 = 折り込み走行中（古い数字）、2巡目以降 = 折り込み済み。
    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(100, true)));
    await mount();
    const first = fetchUsageSeries.mock.calls.length;
    expect(first).toBe(3); // 1画面で3本（時系列 / 機能×モデル / エージェント×モデル）
    expect(folding()).toBe(true);

    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(900, false)));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2500);
    });
    expect(fetchUsageSeries.mock.calls.length).toBe(first + 3);
    expect(folding()).toBe(false);

    // 折り込みが落ちた後は取り直さない（ポーリングに化けさせない）。
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(fetchUsageSeries.mock.calls.length).toBe(first + 3);
  });

  it("自動の取り直しには fold=force を付けない", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(100, true)));
    await mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2500);
    });
    // 付けると折り込みが終わるたびに次を起動して、永久に走り続ける。
    expect(queries().some((q) => q.fold)).toBe(false);
  });

  it("明示的な再取得は fold=force を送る", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(100, false)));
    await mount();
    expect(queries().some((q) => q.fold)).toBe(false);

    const btn = host!.querySelector<HTMLButtonElement>(".uc-reload")!;
    await act(async () => {
      btn.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(queries().filter((q) => q.fold === "force").length).toBe(3);
  });
});

// 金額の面（docs/46 §9-2 の続き）。セッション本体は実測コストを持たないので、単価表 ×
// トークンの**推定**を出す。押さえるのは数字そのものではなく、**推定が実測の顔をしない**
// ことと、**値付けできなかった消費を黙って落とさない**こと。
describe("UsageView の推定額", () => {
  it("推定額は ≈ 付きで出し、実測とは足さない", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(
        series(1000, false, {
          totals: { spend: 1000, in: 1000, out: 0, cread: 0, ccreate: 0, calls: 1, cost_usd: 2, cost_est_usd: 12.5 },
        }),
      ),
    );
    await mount();
    // 12.5 と 2 を足した "14.50" が出てはいけない（別の計測法を1つの数字に混ぜない）。
    expect(kpiText("API換算相当額")).toBe("≈$12.50");
  });

  it("値付けできない消費は割合を添えて申告する", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 750, unpriced_spend: 250 })),
    );
    await mount();
    const cov = host!.querySelector(".usage-coverage")?.textContent || "";
    expect(cov).toContain("25%");
  });

  it("値付けの漏れが無ければ注記は出さない", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 1000, unpriced_spend: 0 })),
    );
    await mount();
    expect(host!.querySelector(".usage-coverage")).toBe(null);
  });
});

// 単価カタログ（docs/46 §5-c / P2a）。金額だけ出して出所を言わないと検算できないので、
// 「どの単価で・どこ由来か」と「いつ時点のカタログか」が画面に出ることを見る。
describe("UsageView の単価の出所", () => {
  const priced = (): UsageSeries => ({
    ...series(1000, false, {
      priced_spend: 1000,
      unpriced_spend: 0,
      catalog: { origin: "opencode", models: 518, fetched: "2026-08-31T03:29:43Z" },
      prices: {
        "gpt-5.6-terra": { src: "catalog:openai/gpt-5.6-terra", in: 2, out: 12, cread: 0.2, cwrite: 2.5 },
      },
    }),
    matrix: {
      session: {
        "gpt-5.6-terra": { spend: 1000, in: 1000, out: 0, cread: 0, ccreate: 0, calls: 1, cost_est_usd: 0.002 },
      },
    },
  });

  it("金額セルのツールチップに単価と出所が出る", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(priced()));
    await mount();
    const title = [...host!.querySelectorAll("td.num")].map((td) => td.getAttribute("title") || "").join("\n");
    expect(title).toContain("$2");
    expect(title).toContain("$12");
    expect(title).toContain("openai/gpt-5.6-terra");
  });

  it("カタログの取得日を出す（更新で過去の額が変わるため）", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(priced()));
    await mount();
    const cov = host!.querySelector(".usage-coverage")?.textContent || "";
    expect(cov).toContain("models.dev");
    expect(cov).toContain("518");
  });

  it("カタログが無ければ注記も出さない", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 1000, unpriced_spend: 0 })),
    );
    await mount();
    expect(host!.querySelector(".usage-coverage")).toBe(null);
  });
});

// 端数の言い方。「消費の 0% は単価を持っていないモデル」は自己矛盾で、実データで出た
// （57k / 34.1M）。1% 未満は専用の言い方にする。
it("1% 未満の値付け漏れを「0%」と書かない", async () => {
  fetchUsageSeries.mockImplementation(() =>
    Promise.resolve(series(1000, false, { priced_spend: 34_074_000, unpriced_spend: 57_000 })),
  );
  await mount();
  const cov = host!.querySelector(".usage-coverage")?.textContent || "";
  expect(cov).not.toContain("0%");
  expect(cov).toContain("1%");
});
