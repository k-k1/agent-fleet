import { useMemo } from "react";
import { groupRefs, laneColor, laneCount, layoutGraph } from "../../lib/gitgraph.ts";
import type { GraphCommit, GraphRow, RefChip } from "../../lib/gitgraph.ts";
import { relTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";

// CommitGraph renders the lane-layout DAG (codeleaf CommitGraphScreen port). Each row is
// a fixed-height flex line: an SVG graph cell (lanes + edges + node) beside a message
// column (ref chips + subject + author·time·sha). Edges are derived purely from
// (nodeLane, lanesAbove, lanesBelow); colors cycle by lane index.

const ROW_H = 48;
const LANE_W = 14;
const NODE_R = 4;

// relDate: an ISO instant → a short locale-aware "… ago" (delegated to the shared lib/intl).
const relDate = (iso: string): string => relTime(iso);

export function CommitGraph({
  commits,
  current,
  selectedSha,
  onSelect,
  onOpen,
  onContext,
}: {
  commits: GraphCommit[];
  current?: string;
  selectedSha?: string;
  onSelect: (sha: string) => void; // plain click / keyboard select
  onOpen?: (sha: string, newPane: boolean) => void; // Ctrl/⌘/middle-click → open in a pane
  onContext?: (commit: GraphCommit, x: number, y: number) => void;
}) {
  const rows = useMemo(() => layoutGraph(commits), [commits]);
  const lanes = laneCount(rows);
  const graphW = Math.max(LANE_W, lanes * LANE_W);
  return (
    <ul className="cgraph">
      {rows.map((row) => (
        <CommitRow
          key={row.commit.sha}
          row={row}
          graphW={graphW}
          current={current}
          selected={row.commit.sha === selectedSha}
          onSelect={onSelect}
          onOpen={onOpen}
          onContext={onContext}
        />
      ))}
    </ul>
  );
}

function CommitRow({
  row,
  graphW,
  current,
  selected,
  onSelect,
  onOpen,
  onContext,
}: {
  row: GraphRow;
  graphW: number;
  current?: string;
  selected: boolean;
  onSelect: (sha: string) => void;
  onOpen?: (sha: string, newPane: boolean) => void;
  onContext?: (commit: GraphCommit, x: number, y: number) => void;
}) {
  const { commit, nodeLane, lanesAbove, lanesBelow } = row;
  const laneX = (i: number) => LANE_W / 2 + i * LANE_W;
  const cy = ROW_H / 2;
  const nodeX = laneX(nodeLane);
  const segs: React.ReactNode[] = [];

  // Top half: each incoming lane. The lane that carries THIS commit's sha bends into
  // the node; the rest pass straight down to the midline.
  lanesAbove.forEach((sha, j) => {
    if (!sha) return;
    const color = laneColor(j);
    if (sha === commit.sha) {
      segs.push(<line key={"a" + j} x1={laneX(j)} y1={0} x2={nodeX} y2={cy} stroke={color} strokeWidth={2} strokeLinecap="round" />);
    } else {
      segs.push(<line key={"a" + j} x1={laneX(j)} y1={0} x2={laneX(j)} y2={cy} stroke={color} strokeWidth={2} strokeLinecap="round" />);
    }
  });
  // Bottom half, part 1: lanes that merely pass this row by (content unchanged from
  // above) run straight down. They belong to other commits' lines, so they are drawn
  // regardless of what this node does.
  lanesBelow.forEach((sha, j) => {
    if (!sha || j === nodeLane || lanesAbove[j] !== sha) return;
    segs.push(<line key={"b" + j} x1={laneX(j)} y1={cy} x2={laneX(j)} y2={ROW_H} stroke={laneColor(j)} strokeWidth={2} strokeLinecap="round" />);
  });
  // Bottom half, part 2: exactly ONE edge per parent, from the node to the lane that
  // carries that parent downward. The first parent always continues in the node's own
  // lane; a merge's extra parents live wherever layout put them (a fresh lane, or one
  // that was already waiting for them — the join edge a merge needs).
  //
  // Picking the lane per-parent rather than per-lane is what keeps a branch point clean:
  // when a commit has several children, every child's lane is simultaneously waiting for
  // it, and a lane-driven walk would emit a redundant diagonal from each sibling lane on
  // top of the node's own line (both colors leaving one node). The lanes converge once,
  // at the parent's own row, via the bend-in edges drawn by the top half above.
  const drawn = new Set<number>();
  for (const p of commit.parents) {
    const lane = lanesBelow[nodeLane] === p ? nodeLane : lanesBelow.indexOf(p);
    if (lane < 0 || drawn.has(lane)) continue;
    drawn.add(lane);
    segs.push(<line key={"p" + lane} x1={nodeX} y1={cy} x2={laneX(lane)} y2={ROW_H} stroke={laneColor(lane)} strokeWidth={2} strokeLinecap="round" />);
  }

  const nodeColor = laneColor(nodeLane);
  return (
    <li
      className={"cgraph-row" + (selected ? " active" : "") + (commit.inBranch ? "" : " dim")}
      // Plain click selects; Ctrl/⌘-click opens the detail in a new pane.
      onClick={(e) => (e.ctrlKey || e.metaKey ? onOpen?.(commit.sha, true) : onSelect(commit.sha))}
      // Middle-click also opens in a new pane (suppress the autoscroll on mousedown).
      onMouseDown={(e) => e.button === 1 && e.preventDefault()}
      onAuxClick={(e) => {
        if (e.button === 1) {
          e.preventDefault();
          onOpen?.(commit.sha, true);
        }
      }}
      onContextMenu={
        onContext
          ? (e) => {
              e.preventDefault();
              onContext(commit, e.clientX, e.clientY);
            }
          : undefined
      }
      title={commit.subject}
    >
      <svg className="cgraph-svg" width={graphW} height={ROW_H} style={{ flex: `0 0 ${graphW}px` }} aria-hidden="true">
        {segs}
        <circle
          cx={nodeX}
          cy={cy}
          r={NODE_R}
          fill={commit.inBranch ? nodeColor : "var(--bg)"}
          stroke={nodeColor}
          strokeWidth={2}
        />
      </svg>
      <div className="cgraph-msg">
        <div className="cgraph-line1">
          {groupRefs(commit.refs).map((chip, i) => (
            <RefBadge key={i} chip={chip} current={current} />
          ))}
          <span className="cgraph-subj">{commit.subject}</span>
        </div>
        <div className="cgraph-line2">
          {commit.author} · {relDate(commit.date)} · <code>{commit.short}</code>
        </div>
      </div>
    </li>
  );
}

// RefBadge draws one grouped chip (lib/gitgraph groupRefs). The kind is carried by a
// leading icon rather than spelled into the label — a tag reads "🏷 v0.5.0", not
// "refs/tags/v0.5.0" — and a local branch whose remotes sit on this same commit gets a
// cloud marker instead of a second "origin/…" chip beside it.
function RefBadge({ chip, current }: { chip: RefChip; current?: string }) {
  const tr = useT();
  const isCurrent = chip.type === "head" && chip.name === current;
  const icon = chip.type === "tag" ? "tag" : chip.type === "remote" ? "cloud" : "git-branch";
  // The marker stays short so the BRANCH NAME keeps the chip's width: "origin" is the
  // near-universal remote and the cloud alone says it; a single other remote is worth
  // naming; several collapse to a count, with the tooltip carrying the full list.
  const named =
    chip.remotes.length > 1 ? `×${chip.remotes.length}` : chip.remotes[0] === "origin" ? "" : (chip.remotes[0] ?? "");
  const title =
    chip.type === "tag"
      ? tr("scm.ref_tag", { name: chip.name })
      : chip.type === "remote"
        ? tr("scm.ref_remote", { name: chip.name })
        : chip.remotes.length > 0
          ? tr("scm.ref_synced", { name: chip.name, remotes: chip.remotes.map((r) => `${r}/${chip.name}`).join(", ") })
          : tr("scm.ref_local", { name: chip.name });
  return (
    <span className={"cgraph-ref ref-" + chip.type + (isCurrent ? " current" : "")} title={title}>
      <Icon name={icon} className="cgraph-ref-ic" />
      <span className="cgraph-ref-name">{chip.name}</span>
      {chip.remotes.length > 0 && (
        <span className="cgraph-ref-sync">
          <Icon name="cloud" />
          {named}
        </span>
      )}
    </span>
  );
}
