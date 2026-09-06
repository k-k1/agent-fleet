// The one thing this chart must not do is draw through a hole. A missing sample means
// "nobody observed the value then"; joining across it invents a smooth line over exactly the
// period the evidence is missing, and nothing on screen would show that it happened.
import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { TrendChart } from "./TrendChart.tsx";

const at = (mins: number) => 1_700_000_000_000 + mins * 60_000;
const polylines = (html: string) => [...html.matchAll(/class="trend-line" points="([^"]*)"/g)].map((m) => m[1]);

describe("TrendChart", () => {
  it("draws one unbroken line for a continuous series", () => {
    const points = [0, 1, 2, 3].map((i) => ({ t: at(0) + i * 4000, v: 10 * i }));
    const html = renderToStaticMarkup(<TrendChart points={points} spanMs={60_000} max={100} />);
    expect(polylines(html).length).toBe(1);
  });

  it("breaks the line at an explicit gap", () => {
    const points = [
      { t: at(0), v: 10 },
      { t: at(0) + 4000, v: 20 },
      { t: at(0) + 8000, v: null },
      { t: at(0) + 12000, v: 30 },
    ];
    expect(polylines(renderToStaticMarkup(<TrendChart points={points} spanMs={60_000} max={100} />)).length).toBe(2);
  });

  // A background tab's timers drop to about once a minute, so samples arrive far apart with
  // no null between them. Time, not index, is what says they are not neighbours.
  it("breaks the line across a long silence even with no null", () => {
    const points = [
      { t: at(0), v: 10 },
      { t: at(0) + 4000, v: 20 },
      { t: at(3), v: 30 }, // three minutes later
    ];
    expect(polylines(renderToStaticMarkup(<TrendChart points={points} spanMs={600_000} max={100} />)).length).toBe(2);
  });

  it("drops samples older than the window", () => {
    const points = [
      { t: at(0), v: 99 },
      { t: at(30), v: 10 },
      { t: at(30) + 4000, v: 20 },
    ];
    // The window ends at the newest sample, so the 30-minute-old one is outside a 1-minute span.
    const line = polylines(renderToStaticMarkup(<TrendChart points={points} spanMs={60_000} max={100} />));
    expect(line.length).toBe(1);
    expect(line[0].split(" ").length).toBe(2);
  });

  it("still shows a lone sample after a gap", () => {
    const points = [
      { t: at(0), v: null },
      { t: at(0) + 4000, v: 42 },
    ];
    const html = renderToStaticMarkup(<TrendChart points={points} spanMs={60_000} max={100} />);
    expect(polylines(html).length).toBe(1);
    expect(html).toContain("trend-area");
  });
});
