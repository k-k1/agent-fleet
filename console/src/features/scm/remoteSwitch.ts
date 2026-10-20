// Switch targets a commit's remote-tracking refs offer in the commit-graph menu.
import type { GraphRef } from "../../lib/gitgraph.ts";

export interface RemoteSwitchTarget {
  /** The ref as drawn, e.g. `origin/main`. */
  ref: string;
  remote: string;
  branch: string;
}

/** remoteSwitchTargets lists the remote refs worth a "switch" entry. The agent
 * splits each ref on the configured remote names (remote and branch names can both
 * contain slashes), so refs it could not attribute to a remote are skipped, as are
 * the remote's symbolic HEAD and branches that also have a local head on the same
 * commit (already offered as a plain switch). */
export function remoteSwitchTargets(refs: GraphRef[]): RemoteSwitchTarget[] {
  const local = new Set(refs.filter((r) => r.type === "head").map((r) => r.name));
  const out: RemoteSwitchTarget[] = [];
  for (const r of refs) {
    if (r.type !== "remote" || !r.remote || !r.branch) continue;
    if (r.branch === "HEAD" || local.has(r.branch)) continue;
    out.push({ ref: r.name, remote: r.remote, branch: r.branch });
  }
  return out;
}
