// pinDrift — drift detection for the tool-version table in Settings > Environment. Compares the
// version actually installed against the pin baked at image build time (versions.json) and returns
// the direction the coloured badge should show. Version shapes vary a lot (semver 2.1.220, dated
// builds like 2026.07.23-e383d2b, a fetch-failure placeholder, "(timeout)"), so only the numeric
// segments are compared in order and anything undecidable falls back to unknown, i.e. no badge.
// behind = older than the pin (an update has not arrived; warning colour); ahead = newer than the
// pin (moved forward by self-update or similar; informational colour).

export type PinDrift = "behind" | "ahead" | "same" | "unknown";

export function pinDrift(version: string | undefined, pin: string | undefined): PinDrift {
  const a = segs(version);
  const b = segs(pin);
  if (!a || !b) return "unknown";
  const n = Math.max(a.length, b.length);
  for (let i = 0; i < n; i++) {
    // A missing segment counts as 0, so "1.2" < "1.2.3". segs drops the sha suffix of a cursor pin
    // like 2026.07.23-e383d2b, so it compares equal to the effective 2026.07.23 that extractVer
    // produces when the dates match.
    const x = a[i] ?? 0;
    const y = b[i] ?? 0;
    if (x < y) return "behind";
    if (x > y) return "ahead";
  }
  return "same";
}

// Reduce to a list of numeric segments: take the leading run of numbers and stop at the first
// non-numeric one, treating the rest as a suffix such as a build sha. A value with no leading
// number yields null, meaning undecidable.
function segs(v: string | undefined): number[] | null {
  if (!v) return null;
  const out: number[] = [];
  for (const p of v.trim().split(/[.-]/)) {
    if (!/^\d+$/.test(p)) break;
    out.push(parseInt(p, 10));
  }
  return out.length ? out : null;
}
