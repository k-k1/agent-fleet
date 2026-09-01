// 稼働時間ヒートマップの描画（docs/log/83）。
//
// 見るのは**マスの顔つき**で、数字の中身ではない。3 値（未観測 / 停止 / 稼働）が
// 3 通りの見た目になっていることが、この画面の主張そのものだからである。潰れると、
// CP が落ちていた日が「誰も働かなかった日」として自信たっぷりに表示される。
import { describe, it, expect, afterEach, beforeAll, afterAll, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (p: string) => api(p),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

const { UptimeHeatmap } = await import("./UptimeHeatmap.tsx");

const ORIGINAL_TZ = process.env.TZ;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeAll(() => {
  process.env.TZ = "Asia/Tokyo";
  g.IS_REACT_ACT_ENVIRONMENT = true;
});
afterAll(() => {
  process.env.TZ = ORIGINAL_TZ;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

// UTC 00 時 = ローカル 9 時（Asia/Tokyo）。ローカルで見ると:
//   09 時 セッション 3 本・動いていたのは 1 本
//   10 時 セッション 2 本・動いていたのは 0 本  ← 指標を変えると順位が入れ替わる
//   11 時 稼働中だが Agent に届かず本数不明
//   12 時 観測できたが停止
//   13 時以降 ハートビートも無い＝未観測
const DATA = {
  from: "2026-09-01",
  to: "2026-09-01",
  interval_secs: 300,
  observed: [
    { hour: "2026-09-01T00", samples: 12 },
    { hour: "2026-09-01T01", samples: 12 },
    { hour: "2026-09-01T02", samples: 12 },
    { hour: "2026-09-01T03", samples: 12 },
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
          session_secs: 10800,
          busy_secs: 3600,
          max_sessions: 3,
          max_busy: 1,
        },
        {
          hour: "2026-09-01T01",
          samples: 12,
          running_secs: 3600,
          measured_secs: 3600,
          session_secs: 7200,
          busy_secs: 0,
          max_sessions: 2,
          max_busy: 0,
        },
        // 起きてはいたが Agent に届かなかった時間。
        { hour: "2026-09-01T02", samples: 12, running_secs: 3600 },
      ],
    },
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const mount = async (data: unknown = DATA) => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<UptimeHeatmap data={data as never} from="2026-09-01" to="2026-09-01" />);
  });
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

// 1 行目は日付の見出し、その下が 24 行 × 日数。
const cells = () => [...(host?.querySelectorAll(".uh-cell") || [])];
const cellAt = (hour: number) => cells()[hour];

describe("マスの 3 値", () => {
  it("24 時間ぶんのマスを 1 日ぶん出す", async () => {
    await mount();
    expect(cells().length).toBe(24);
  });

  it("稼働・停止・未観測が別の見た目になる", async () => {
    await mount();
    // 9 時（UTC 00）は稼働。段は 1..5 のどれか。
    expect(cellAt(9)!.className).toMatch(/uh-lv-[1-5]/);
    // 12 時（UTC 03）はハートビートだけ＝停止。
    expect(cellAt(12)!.className).toContain("uh-stopped");
    // 13 時（UTC 04）はハートビートも無い＝未観測。
    expect(cellAt(13)!.className).toContain("uh-unobserved");
  });

  // ⚠️ 起きていたが本数が分からない時間を「0 本」と同じ見た目にしない。
  it("本数不明の時間には地模様が付く", async () => {
    await mount();
    expect(cellAt(11)!.className).toContain("uh-unmeasured");
  });

  // 色だけで意味を運ばない。読み上げでも「9/1 09:00 3.0」と読める。
  it("状態は aria-label にも入る", async () => {
    await mount();
    expect(cellAt(12)!.getAttribute("aria-label")).toContain("停止");
    expect(cellAt(13)!.getAttribute("aria-label")).toContain("記録なし");
  });

  it("応答が無くても落ちず、全部が未観測になる", async () => {
    await mount(null);
    expect(cells().length).toBe(24);
    expect(cells().every((c) => c.className.includes("uh-unobserved"))).toBe(true);
  });
});

describe("指標の切り替え", () => {
  // 濃さは「その指標の最大」に対する相対なので、指標を変えるとマスの順位が入れ替わる。
  // 10 時はセッション 2 本（濃い）だが、動いていた本数は 0 本（最下段）。
  it("動いていた本数に切り替えるとマスの順位が入れ替わる", async () => {
    await mount();
    expect(cellAt(10)!.className).toContain("uh-lv-4");
    const busy = [...(host?.querySelectorAll(".uh-metric") || [])].find((b) =>
      b.textContent?.includes("動いていた"),
    ) as HTMLButtonElement;
    await act(async () => {
      busy.click();
    });
    expect(cellAt(10)!.className).toContain("uh-lv-1");
    expect(cellAt(9)!.className).toContain("uh-lv-5");
    expect(busy.getAttribute("aria-pressed")).toBe("true");
  });
});

describe("色を見分けられない読み手への逃げ道", () => {
  it("表ビューに切り替えると値が数字で出る", async () => {
    await mount();
    const btn = [...(host?.querySelectorAll(".uh-tablebtn") || [])][0] as HTMLButtonElement;
    await act(async () => {
      btn.click();
    });
    const rows = [...(host?.querySelectorAll(".uh-table tbody tr") || [])];
    // 稼働していた 3 時間ぶん（09・10・11 時）。停止と未観測は行にしない。
    expect(rows.length).toBe(3);
    expect(rows[0].textContent).toContain("2026-09-01 09:00");
    // 本数が分からない時間は数字ではなくその旨が出る。
    expect(rows[2].textContent).toContain("本数不明");
  });

  it("凡例は常に出る", async () => {
    await mount();
    expect(host?.querySelectorAll(".uh-legend .uh-swatch").length).toBe(7);
  });
});
