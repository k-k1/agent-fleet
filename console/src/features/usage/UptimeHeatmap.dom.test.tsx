// Rendering of the uptime heatmap (docs/log/83).
//
// What is checked is how a cell looks, not the numbers in it: the whole claim of this view is that
// the three values (unobserved / stopped / running) look three different ways. Collapse them and a
// day the CP was down is confidently drawn as a day nobody worked.
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

// Pin the timezone in the module body. In `beforeAll` it runs after describe collection, so any
// code that reads the clock at collection time is green on the developer's machine only.
const ORIGINAL_TZ = process.env.TZ;
process.env.TZ = "Asia/Tokyo";
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeAll(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
});
afterAll(() => {
  process.env.TZ = ORIGINAL_TZ;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

// UTC 00 = local 09:00 (Asia/Tokyo). Read locally:
//   09:00 3 sessions, 1 of them busy
//   10:00 2 sessions, 0 of them busy  <- switching the metric reorders these cells
//   11:00 running, but the Agent was unreachable so the count is unknown
//   12:00 observed but stopped
//   13:00 onwards no heartbeat either = unobserved
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
        // An hour that was up but where the Agent could not be reached.
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

// The first row is the date header; below it are 24 rows per day.
const cells = () => [...(host?.querySelectorAll(".uh-cell") || [])];
const cellAt = (hour: number) => cells()[hour];

describe("the three values a cell can have", () => {
  it("renders 24 cells for one day", async () => {
    await mount();
    expect(cells().length).toBe(24);
  });

  it("makes running, stopped and unobserved look different", async () => {
    await mount();
    // 09:00 (UTC 00) was running; the level is one of 1..5.
    expect(cellAt(9)!.className).toMatch(/uh-lv-[1-5]/);
    // 12:00 (UTC 03) has only a heartbeat = stopped.
    expect(cellAt(12)!.className).toContain("uh-stopped");
    // 13:00 (UTC 04) has no heartbeat either = unobserved.
    expect(cellAt(13)!.className).toContain("uh-unobserved");
  });

  // An hour that was up but whose session count is unknown must not look like a count of zero.
  it("hatches the cells whose session count is unknown", async () => {
    await mount();
    expect(cellAt(11)!.className).toContain("uh-unmeasured");
  });

  // Meaning never rides on colour alone; a screen reader reads out "9/1 09:00 3.0" as well.
  it("puts the state in the aria-label too", async () => {
    await mount();
    expect(cellAt(12)!.getAttribute("aria-label")).toContain("停止");
    expect(cellAt(13)!.getAttribute("aria-label")).toContain("記録なし");
  });

  it("survives an empty response and marks everything unobserved", async () => {
    await mount(null);
    expect(cells().length).toBe(24);
    expect(cells().every((c) => c.className.includes("uh-unobserved"))).toBe(true);
  });
});

describe("switching the metric", () => {
  // Intensity is relative to the maximum of the chosen metric, so changing it reorders the cells.
  // 10:00 has 2 sessions (dark) but 0 busy ones (the lowest level).
  it("reorders the cells when switched to the busy-session count", async () => {
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

describe("the way out for a reader who cannot tell the colours apart", () => {
  it("shows the values as numbers when switched to the table view", async () => {
    await mount();
    const btn = [...(host?.querySelectorAll(".uh-tablebtn") || [])][0] as HTMLButtonElement;
    await act(async () => {
      btn.click();
    });
    const rows = [...(host?.querySelectorAll(".uh-table tbody tr") || [])];
    // The three hours that were running (09, 10, 11). Stopped and unobserved get no row.
    expect(rows.length).toBe(3);
    expect(rows[0].textContent).toContain("2026-09-01 09:00");
    // An hour with an unknown count says so instead of printing a number.
    expect(rows[2].textContent).toContain("本数不明");
  });

  it("always renders the legend", async () => {
    await mount();
    expect(host?.querySelectorAll(".uh-legend .uh-swatch").length).toBe(7);
  });
});
