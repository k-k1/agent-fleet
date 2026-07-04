// Commit-graph lane layout — a faithful TS port of codeleaf's GitGraphLayout.kt.
// Given commits newest-first (each carrying its parent SHAs), it assigns every commit
// a column ("lane") and records, for each row, which SHA every lane is waiting for as
// it enters from above and leaves below. The renderer derives all edges purely from
// (nodeLane, lanesAbove, lanesBelow) — see CommitGraph.tsx.

export interface GraphRef {
  name: string;
  type: "head" | "remote" | "tag";
}

export interface GraphCommit {
  sha: string;
  short: string;
  parents: string[];
  author: string;
  date: string;
  subject: string;
  refs: GraphRef[];
  inBranch: boolean;
}

export interface GraphRow {
  commit: GraphCommit;
  nodeLane: number; // column the commit's dot sits in
  lanesAbove: (string | null)[]; // per-lane SHA expected from above (null = empty)
  lanesBelow: (string | null)[]; // per-lane SHA leaving downward
}

// nearestEmptyLane returns the empty lane closest to `node`; on a tie it prefers the
// larger index (right), keeping low lanes free for long-lived branches. -1 if none.
function nearestEmptyLane(active: (string | null)[], node: number): number {
  let best = -1;
  let bestDist = Infinity;
  for (let i = 0; i < active.length; i++) {
    if (active[i] !== null) continue;
    const d = Math.abs(i - node);
    if (d < bestDist || (d === bestDist && i > best)) {
      best = i;
      bestDist = d;
    }
  }
  return best;
}

// layoutGraph assigns lanes to commits (given newest-first). `active[lane]` holds the
// SHA that lane is currently waiting for. First-parent continuity keeps the mainline
// vertical; extra (merge) parents take the nearest empty lane to reduce crossings.
export function layoutGraph(commits: GraphCommit[]): GraphRow[] {
  const rows: GraphRow[] = [];
  const active: (string | null)[] = [];
  for (const c of commits) {
    const above = active.slice();

    // 1. Node lane: a lane already waiting for this SHA; else leftmost empty; else append.
    let nodeLane = active.indexOf(c.sha);
    if (nodeLane < 0) {
      nodeLane = active.indexOf(null);
      if (nodeLane < 0) {
        active.push(null);
        nodeLane = active.length - 1;
      }
    }
    // 2. Other lanes also waiting for this SHA (children merging in) collapse to the node.
    for (let k = 0; k < active.length; k++) {
      if (k !== nodeLane && active[k] === c.sha) active[k] = null;
    }
    // 3. Node lane continues to the FIRST parent (frees the lane if this is a root).
    active[nodeLane] = c.parents.length > 0 ? c.parents[0] : null;
    // 4. Additional (merge) parents: reuse a lane already waiting for them, else nearest empty.
    for (let pi = 1; pi < c.parents.length; pi++) {
      const p = c.parents[pi];
      if (active.indexOf(p) >= 0) continue;
      let lane = nearestEmptyLane(active, nodeLane);
      if (lane < 0) {
        active.push(null);
        lane = active.length - 1;
      }
      active[lane] = p;
    }
    // 5. Trim trailing empty lanes to keep the drawing width minimal.
    while (active.length > 0 && active[active.length - 1] === null) active.pop();

    rows.push({ commit: c, nodeLane, lanesAbove: above, lanesBelow: active.slice() });
  }
  return rows;
}

// laneCount is the drawing width: the widest point across all rows.
export function laneCount(rows: GraphRow[]): number {
  let n = 0;
  for (const r of rows) {
    n = Math.max(n, r.nodeLane + 1, r.lanesAbove.length, r.lanesBelow.length);
  }
  return n;
}

// 8-color lane palette (codeleaf LANE_COLORS), cycled by lane index.
export const LANE_COLORS = [
  "#42A5F5", "#66BB6A", "#EF5350", "#AB47BC",
  "#FFA726", "#26C6DA", "#EC407A", "#9CCC65",
];
export const laneColor = (lane: number): string => LANE_COLORS[((lane % 8) + 8) % 8];
