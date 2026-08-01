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

// RefChip is one ref badge as DRAWN, which is not one-to-one with the refs git reports:
// a local branch and the remote-tracking refs of the same name sitting on the same commit
// are the normal case (nothing to push/pull) and collapse into a single chip carrying a
// remote marker, instead of eating two chips' width with "develop" + "origin/develop".
// A remote that has drifted decorates a DIFFERENT commit, so it keeps its own chip there —
// which is exactly the state worth seeing.
export interface RefChip {
  type: "head" | "remote" | "tag";
  name: string; // display label: branch short name / tag name / "origin/x" for remote-only
  remotes: string[]; // remotes in sync with this local branch here ([] when none / not a head)
  refs: GraphRef[]; // the raw refs this chip stands for (tooltips, actions)
}

// groupRefs collapses a commit's refs into display chips (see RefChip). Order follows the
// refs as given, each chip taking the position of its first member.
export function groupRefs(refs: GraphRef[]): RefChip[] {
  const heads = new Set(refs.filter((r) => r.type === "head").map((r) => r.name));
  const chips: RefChip[] = [];
  const byBranch = new Map<string, RefChip>();
  const chipFor = (branch: string): RefChip => {
    let chip = byBranch.get(branch);
    if (!chip) {
      chip = { type: "head", name: branch, remotes: [], refs: [] };
      byBranch.set(branch, chip);
      chips.push(chip);
    }
    return chip;
  };
  for (const rf of refs) {
    if (rf.type === "head") {
      chipFor(rf.name).refs.push(rf);
      continue;
    }
    if (rf.type !== "remote") {
      chips.push({ type: rf.type, name: rf.name, remotes: [], refs: [rf] });
      continue;
    }
    // The backend emits remote refs as "<remote>/<branch>" (from refs/remotes/…), so the
    // first path element is the remote name and the rest is the branch — which is why the
    // split is on the FIRST slash: "origin/feat/x" is remote "origin", branch "feat/x".
    const slash = rf.name.indexOf("/");
    const remote = slash > 0 ? rf.name.slice(0, slash) : "";
    const branch = rf.name.slice(slash + 1);
    if (!remote || !heads.has(branch)) {
      chips.push({ type: "remote", name: rf.name, remotes: [], refs: [rf] });
      continue;
    }
    const chip = chipFor(branch);
    chip.remotes.push(remote);
    chip.refs.push(rf);
  }
  return chips;
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
