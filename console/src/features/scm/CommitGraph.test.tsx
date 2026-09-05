import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { CommitGraph } from "./CommitGraph.tsx";
import type { GraphCommit } from "../../lib/gitgraph.ts";

const c = (sha: string, parents: string[]): GraphCommit => ({
  sha,
  short: sha,
  parents,
  author: "k-k1",
  date: "2026-07-13T00:00:00+09:00",
  subject: sha,
  refs: [],
  inBranch: true,
});

// laneX(i) = 14/2 + i*14, cy = 24, rowH = 48 (follows the constants in CommitGraph.tsx)
const laneX = (i: number) => 7 + i * 14;

// Cut the markup into per-row HTML. The separator must be "<li " WITH the trailing space:
// a bare "<li" also matches the inner <line> elements and shreds the rows.
const renderRows = (commits: GraphCommit[]): string[] =>
  renderToStaticMarkup(<CommitGraph commits={commits} onSelect={() => {}} />)
    .split("<li ")
    .slice(1);

describe("CommitGraph merge edges", () => {
  // The merge's second parent is already awaited in another lane (the parent has several
  // children and one of them was drawn first). The layout adds no lane, but the renderer
  // must still draw a join edge from the node into that existing lane.
  it("draws a join edge to a pass-through lane that is also a parent", () => {
    const commits = [
      c("b88cf42", ["03e1d78"]),
      c("0377c44", ["6eb9b35", "03e1d78"]), // merge; 2nd parent already active in lane0
      c("03e1d78", ["18d0e57"]),
      c("18d0e57", []),
    ];
    const mergeRow = renderRows(commits)[1]; // 0377c44 — node sits in lane1

    // join edge from the node (lane1, centre) down to the foot of lane0
    expect(mergeRow).toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(0)}" y2="48"`);
    // lane0's pass-through straight line stays too (b88cf42→03e1d78 continuing)
    expect(mergeRow).toContain(`x1="${laneX(0)}" y1="24" x2="${laneX(0)}" y2="48"`);
  });

  // A normal merge whose parent takes a lane of its own (regression guard): edges fan out
  // from the node.
  it("still fans out to a freshly assigned parent lane", () => {
    const commits = [
      c("m1", ["p1", "p2"]),
      c("p1", []),
      c("p2", []),
    ];
    const mergeRow = renderRows(commits)[0]; // m1 @lane0, p2 → lane1
    expect(mergeRow).toContain(`x1="${laneX(0)}" y1="24" x2="${laneX(1)}" y2="48"`);
  });
});

// A branch point (one parent, several children). Every child holds a lane awaiting that
// parent, so on any child's row the sibling lanes await the same parent. Drawing a join edge
// into a sibling lane there emits the own-lane line AND a diagonal in the sibling's colour —
// two lines in two colours — which the next row pulls back into a ">"-shaped artefact. The
// line must run straight down its own lane and converge once, on the parent's row.
describe("CommitGraph branch points", () => {
  // b1 and b2 fork from the same parent, base. b1@lane0 / b2@lane1.
  const commits = [
    c("b1", ["base"]),
    c("b2", ["base"]),
    c("base", []),
  ];
  it("does not draw a redundant edge into a sibling lane awaiting the same parent", () => {
    const b2Row = renderRows(commits)[1]; // b2 @lane1; lane0 awaits base as well
    // only the straight line down its own lane (lane1) is present
    expect(b2Row).toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(1)}" y2="48"`);
    // no diagonal joining into lane0 — that was the "two lines in two colours"
    expect(b2Row).not.toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(0)}" y2="48"`);
  });

  it("converges the sibling lanes exactly once, at the shared parent's row", () => {
    const baseRow = renderRows(commits)[2]; // base @lane0: both lane0 and lane1 bend in
    expect(baseRow).toContain(`x1="${laneX(0)}" y1="0" x2="${laneX(0)}" y2="24"`);
    expect(baseRow).toContain(`x1="${laneX(1)}" y1="0" x2="${laneX(0)}" y2="24"`);
  });

  it("emits one edge per parent — never one per lane holding it", () => {
    // A branch point with five children (the shape of a7d5f90 in the real repository). The
    // downward edge out of each child's row is the single line down its own lane; it does
    // not grow one diagonal per sibling lane.
    const many = [
      c("k1", ["root"]), c("k2", ["root"]), c("k3", ["root"]),
      c("k4", ["root"]), c("k5", ["root"]), c("root", []),
    ];
    const k5 = renderRows(many)[4]; // the last child: lanes 0-3 await root too
    // Look only at the edges leaving the node (lane4) downward. The siblings' straight
    // pass-through lines in lanes 0-3 (x1===x2) are a different thing and belong there.
    const fromNode = [...k5.matchAll(new RegExp(`x1="${laneX(4)}" y1="24" x2="(\\d+)" y2="48"`, "g"))];
    expect(fromNode.map((m) => m[1])).toEqual([String(laneX(4))]); // exactly one, its own lane
  });
});
