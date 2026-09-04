import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { stackModel } from "./series.ts";
import type { UsageBucket } from "./api.ts";

// UsageView pulls in core/api/client.ts, which touches browser globals at module init
// (localStorage / window.fetch, absent under vitest's node environment). Stub just enough for the
// import, then import dynamically. The rule that the pure layer (series.ts / colors.ts) depends
// on api.ts for types only still holds; the view itself may keep assuming a browser.
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
  // Regression guard: when the only feature present has no colour slot, the sole series is
  // "other". Hiding the legend below two series would leave an unnamed grey bar and no way to
  // tell what was consumed.
  it("shows the OTHER entry even when it is the only series", () => {
    const html = legendHTML({ "assistant.ask": 100 });
    expect(html).toContain("ulg-sw");
    expect(html).toContain("var(--viz-other)");
    // The real folded keys go in the tooltip, so identity is never carried by colour alone.
    expect(html).toContain(dimLabel("feature", "assistant.ask"));
  });

  // With nothing folded and only one series there is no legend: a single-colour bar needs none.
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
