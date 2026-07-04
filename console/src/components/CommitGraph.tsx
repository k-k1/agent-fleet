import { useMemo } from "react";
import { laneColor, laneCount, layoutGraph } from "../lib/gitgraph.js";
import type { GraphCommit, GraphRow } from "../lib/gitgraph.js";

// CommitGraph renders the lane-layout DAG (codeleaf CommitGraphScreen port). Each row is
// a fixed-height flex line: an SVG graph cell (lanes + edges + node) beside a message
// column (ref chips + subject + author·time·sha). Edges are derived purely from
// (nodeLane, lanesAbove, lanesBelow); colors cycle by lane index.

const ROW_H = 48;
const LANE_W = 14;
const NODE_R = 4;

// relDate: an ISO instant → short Japanese "…前".
function relDate(iso: string): string {
  const t = new Date(iso).getTime();
  if (isNaN(t)) return "";
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 60) return "たった今";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}分前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}時間前`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}日前`;
  const mo = Math.floor(d / 30);
  if (mo < 12) return `${mo}ヶ月前`;
  return `${Math.floor(mo / 12)}年前`;
}

export default function CommitGraph({
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
  // Bottom half: each outgoing lane. A lane unchanged from above passes straight down;
  // the node's parents fan out from the node to their lanes.
  lanesBelow.forEach((sha, j) => {
    if (!sha) return;
    const color = laneColor(j);
    const passThrough = lanesAbove[j] === sha && j !== nodeLane;
    if (passThrough) {
      segs.push(<line key={"b" + j} x1={laneX(j)} y1={cy} x2={laneX(j)} y2={ROW_H} stroke={color} strokeWidth={2} strokeLinecap="round" />);
    } else {
      segs.push(<line key={"b" + j} x1={nodeX} y1={cy} x2={laneX(j)} y2={ROW_H} stroke={color} strokeWidth={2} strokeLinecap="round" />);
    }
  });

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
          {commit.refs.map((rf, i) => (
            <span
              key={i}
              className={"cgraph-ref ref-" + rf.type + (rf.type === "head" && rf.name === current ? " current" : "")}
            >
              {rf.name}
            </span>
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
