import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { stackModel } from "./series.ts";
import type { UsageBucket } from "./api.ts";

// UsageView は core/api/client.ts を引き込み、**モジュール初期化でブラウザ globals を触る**
// （localStorage / window.fetch — node 環境の vitest には無い）。import 時に必要な分だけ
// スタブしてから動的 import する。純関数層（series.ts / colors.ts）を api.ts から型だけに
// 保つ規約はそのままで、View 本体はブラウザ前提のままでよい。
const store = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, String(v)),
  removeItem: (k: string) => void store.delete(k),
  clear: () => store.clear(),
  key: () => null,
  length: 0,
});
vi.stubGlobal("window", {
  fetch: globalThis.fetch,
  addEventListener: () => {},
  removeEventListener: () => {},
  location: { href: "http://localhost/", search: "" },
});
const { Legend, dimLabel } = await import("./UsageView.tsx");

const bucket = (series: Record<string, number>): UsageBucket[] => [
  {
    t: "2026-07-26T00:00:00Z",
    series: Object.fromEntries(
      Object.entries(series).map(([k, spend]) => [k, { spend, in: spend, out: 0, cread: 0, ccreate: 0, calls: 1 }]),
    ),
  },
];

const legendHTML = (series: Record<string, number>, by = "feature"): string => {
  const stack = stackModel(bucket(series), by, "spend");
  return renderToStaticMarkup(
    <Legend stack={stack} by={by} onPick={() => {}} isOn={() => false} />,
  );
};

describe("Legend", () => {
  // ★回帰: 色スロットを持たない feature が1つだけ出ると、系列は「その他」1本になる。
  // 「2系列未満なら凡例なし」だと**名前の無いグレーの棒**だけが残り、何の消費か読めない。
  it("shows the OTHER entry even when it is the only series", () => {
    const html = legendHTML({ "assistant.ask": 100 });
    expect(html).toContain("ulg-sw");
    expect(html).toContain("var(--viz-other)");
    // 畳まれた実キーはツールチップに出す（色だけに identity を持たせない）。
    expect(html).toContain(dimLabel("feature", "assistant.ask"));
  });

  // 畳み込みが無く系列も1本なら凡例は出さない（1色の棒に凡例は要らない）。
  it("stays hidden for a single coloured series", () => {
    expect(legendHTML({ session: 100 })).toBe("");
  });

  it("lists every coloured series in slot order", () => {
    const html = legendHTML({ compact: 10, session: 100, "assistant.chat": 50 });
    const order = ["session", "assistant.chat", "compact"].map((k) => html.indexOf(dimLabel("feature", k)));
    expect(order[0]).toBeGreaterThanOrEqual(0);
    expect(order[0]).toBeLessThan(order[1]);
    expect(order[1]).toBeLessThan(order[2]);
  });
});
