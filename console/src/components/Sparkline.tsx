// Sparkline: a tiny inline trend chart (SVG) for the WsBar resource tiles. `data`
// is a series of numbers (null / non-finite entries are dropped). `max` fixes the
// top of the scale — pass 1 for a ratio so "full" means the cap; omit to autoscale
// to the series (right for an unbounded rate like CPU%). `track` draws a faint
// full-height baseline so a fullness metric shows its headroom. Colour follows
// currentColor, so the caller tints the whole tile by threshold.
interface SparklineProps {
  data?: Array<number | null | undefined>;
  max?: number;
  track?: boolean;
  width?: number;
  height?: number;
}

export default function Sparkline({ data, max, track = false, width = 28, height = 14 }: SparklineProps) {
  const pts = (data || []).filter((v): v is number => typeof v === "number" && isFinite(v));
  const svgProps = {
    className: "spark",
    width,
    height,
    viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: "none",
  };
  const trackRect = track ? <rect x="0" y="0" width={width} height={height} className="spark-track" /> : null;
  if (pts.length < 2) return <svg {...svgProps}>{trackRect}</svg>;

  const hi = max != null ? max : Math.max(...pts, 1e-6);
  const dx = width / (pts.length - 1);
  const y = (v: number) => {
    const r = Math.max(0, Math.min(v / hi, 1));
    return +(height - r * (height - 2) - 1).toFixed(2);
  };
  const line = pts.map((v, i) => `${+(i * dx).toFixed(2)},${y(v)}`).join(" ");
  const area = `0,${height} ${line} ${+((pts.length - 1) * dx).toFixed(2)},${height}`;
  return (
    <svg {...svgProps}>
      {trackRect}
      <polygon className="spark-area" points={area} />
      <polyline className="spark-line" points={line} />
    </svg>
  );
}
