// TrendChart — a full-width time series with a grid, the newest sample on the right.
//
// Not Sparkline with bigger numbers. Sparkline is a 28×14 glyph that DROPS null entries and
// scales by index; this one plots by TIME and BREAKS the line where samples are missing.
// Both differences matter here: the series it draws is sampled by a local clock, so a
// throttled background tab or a dead push stream leaves real holes, and joining across them
// would draw a straight line through a period nobody observed.
interface Point {
  /** Wall clock (ms). */
  t: number;
  /** null = not observed at that time. */
  v: number | null;
}

interface TrendChartProps {
  points: Point[];
  /** Top of the scale. Omit to autoscale to the series (right for an unbounded rate). */
  max?: number;
  height?: number;
  /** Horizontal grid divisions. */
  grid?: number;
  /** Samples further apart than this are a hole, not a segment. */
  breakMs?: number;
  /** Time span the x-axis covers, ending now. Shorter than the buffer = a zoom on the
   *  recent past; the caller decides, because "how far back" is a product question. */
  spanMs: number;
  className?: string;
}

// A fixed viewBox stretched to the element's width (preserveAspectRatio="none"), the same
// trick Sparkline uses: the alternative is measuring the DOM on every resize, and a 1px
// stroke stretched horizontally is not visibly different.
const VW = 600;

export function TrendChart({
  points,
  max,
  height = 72,
  grid = 4,
  breakMs = 9000,
  spanMs,
  className,
}: TrendChartProps) {
  const now = points.length ? points[points.length - 1].t : Date.now();
  const t0 = now - spanMs;
  const inWindow = points.filter((p) => p.t >= t0);
  const values = inWindow.map((p) => p.v).filter((v): v is number => typeof v === "number" && isFinite(v));
  const hi = max != null && max > 0 ? max : Math.max(...values, 1e-6);
  const x = (t: number) => +(((t - t0) / spanMs) * VW).toFixed(2);
  const y = (v: number) => +(height - Math.max(0, Math.min(v / hi, 1)) * (height - 2) - 1).toFixed(2);

  // One <polyline> per unbroken run. A run of a single point draws nothing on its own, so it
  // gets a 1px-wide segment — otherwise the very first sample after a gap is invisible.
  const runs: Point[][] = [];
  let cur: Point[] | null = null;
  for (const p of inWindow) {
    // An explicit null ends the run whatever the timing: it means the value at that moment
    // was not observed, which is exactly what must not be drawn through.
    if (p.v == null) {
      cur = null;
      continue;
    }
    const prev = cur?.[cur.length - 1];
    if (!cur || (prev && p.t - prev.t > breakMs)) {
      cur = [p];
      runs.push(cur);
    } else {
      cur.push(p);
    }
  }

  return (
    <svg
      className={"trend" + (className ? " " + className : "")}
      viewBox={`0 0 ${VW} ${height}`}
      height={height}
      preserveAspectRatio="none"
      role="presentation"
    >
      {Array.from({ length: grid - 1 }, (_, i) => {
        const gy = +(((i + 1) / grid) * height).toFixed(2);
        return <line key={i} className="trend-grid" x1="0" y1={gy} x2={VW} y2={gy} />;
      })}
      {runs.map((run, i) => {
        // A lone sample has no length to draw, so it is given a 1-unit segment — otherwise
        // the first reading after a gap is invisible and the chart looks emptier than the
        // data is.
        const pts = run.length > 1 ? run : [run[0], { t: run[0].t, v: run[0].v }];
        const xs = pts.map((p, j) => (run.length > 1 ? x(p.t) : x(p.t) + j));
        const line = pts.map((p, j) => `${xs[j]},${y(p.v as number)}`).join(" ");
        const area = `${xs[0]},${height} ${line} ${xs[xs.length - 1]},${height}`;
        return (
          <g key={i}>
            <polygon className="trend-area" points={area} />
            <polyline className="trend-line" points={line} />
          </g>
        );
      })}
    </svg>
  );
}
