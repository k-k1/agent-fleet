// Rendering of the usage view. These describes check how the numbers present themselves, not
// what the numbers are: freshness first, then the estimated amount.
//
// Freshness is two guarantees against showing a stale number as if it were current:
//   1. Fold-on-read of the sessions themselves is asynchronous, so while it runs the response
//      does not include the most recent turns. While the server returns folding set, this side
//      refetches automatically; without that the screen only catches up after several presses of
//      reload.
//   2. An explicit refetch sends fold=force. Without it the 60-second throttle applies and turns
//      that had already finished when the button was pressed stay out for up to a minute, so
//      pressing it changes nothing.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { UsageQuery, UsageSeries } from "./api.ts";

const fetchUsageSeries = vi.fn();
vi.mock("./api.ts", () => ({
  fetchUsageSeries: (q: UsageQuery, s?: AbortSignal) => fetchUsageSeries(q, s),
  // The rtk savings card is a separate lineage and unrelated to this view's freshness; report
  // it absent so it hides silently.
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

describe("UsageView freshness", () => {
  it("refetches automatically while folding and stops once it is done", async () => {
    // First round = folding in progress (stale numbers), later rounds = folded.
    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(100, true)));
    await mount();
    const first = fetchUsageSeries.mock.calls.length;
    expect(first).toBe(3); // three per screen: time series / feature x model / agent x model
    expect(folding()).toBe(true);

    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(900, false)));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2500);
    });
    expect(fetchUsageSeries.mock.calls.length).toBe(first + 3);
    expect(folding()).toBe(false);

    // Once folding clears there is no refetch; this must not turn into polling.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(fetchUsageSeries.mock.calls.length).toBe(first + 3);
  });

  it("does not add fold=force to an automatic refetch", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(series(100, true)));
    await mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2500);
    });
    // With it, every finished fold would start the next one and folding would never stop.
    expect(queries().some((q) => q.fold)).toBe(false);
  });

  it("sends fold=force on an explicit refetch", async () => {
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

// The money side (continues docs/log/46 §9-2). The sessions themselves carry no measured cost,
// so what is shown is an estimate from the price table times the tokens. What is pinned here is
// not the numbers but that an estimate never wears the face of a measurement, and that
// consumption which could not be priced is never dropped silently.
describe("UsageView estimated amount", () => {
  it("prefixes the estimate with the approximation sign and never adds it to the measured cost", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(
        series(1000, false, {
          totals: { spend: 1000, in: 1000, out: 0, cread: 0, ccreate: 0, calls: 1, cost_usd: 2, cost_est_usd: 12.5 },
        }),
      ),
    );
    await mount();
    // "14.50", i.e. 12.5 plus 2, must never appear: one number never mixes two ways of
    // measuring.
    expect(kpiText("API換算相当額")).toBe("≈$12.50");
  });

  it("declares unpriceable consumption together with its share", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 750, unpriced_spend: 250 })),
    );
    await mount();
    const cov = host!.querySelector(".usage-coverage")?.textContent || "";
    expect(cov).toContain("25%");
  });

  it("shows no note when nothing was left unpriced", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 1000, unpriced_spend: 0 })),
    );
    await mount();
    expect(host!.querySelector(".usage-coverage")).toBe(null);
  });
});

// The price catalogue (docs/log/46 §5-c / P2a). An amount without its source cannot be checked,
// so this pins that the screen shows which price, where it came from, and as of when.
describe("UsageView price provenance", () => {
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

  it("puts the unit price and its source in the tooltip of a money cell", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(priced()));
    await mount();
    const title = [...host!.querySelectorAll("td.num")].map((td) => td.getAttribute("title") || "").join("\n");
    expect(title).toContain("$2");
    expect(title).toContain("$12");
    expect(title).toContain("openai/gpt-5.6-terra");
  });

  it("shows when the catalogue was fetched, since an update changes past amounts", async () => {
    fetchUsageSeries.mockImplementation(() => Promise.resolve(priced()));
    await mount();
    const cov = host!.querySelector(".usage-coverage")?.textContent || "";
    expect(cov).toContain("models.dev");
    expect(cov).toContain("518");
  });

  it("shows no note when there is no catalogue", async () => {
    fetchUsageSeries.mockImplementation(() =>
      Promise.resolve(series(1000, false, { priced_spend: 1000, unpriced_spend: 0 })),
    );
    await mount();
    expect(host!.querySelector(".usage-coverage")).toBe(null);
  });
});

// How to word a small fraction. "0% of consumption is on models with no price" contradicts
// itself and appeared on real data (57k / 34.1M), so anything below 1% gets its own wording.
it("never writes an unpriced share below 1% as 0%", async () => {
  fetchUsageSeries.mockImplementation(() =>
    Promise.resolve(series(1000, false, { priced_spend: 34_074_000, unpriced_spend: 57_000 })),
  );
  await mount();
  const cov = host!.querySelector(".usage-coverage")?.textContent || "";
  expect(cov).not.toContain("0%");
  expect(cov).toContain("1%");
});
