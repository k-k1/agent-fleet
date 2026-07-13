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

// laneX(i) = 14/2 + i*14, cy = 24, rowH = 48 (CommitGraph.tsx の定数に追従)
const laneX = (i: number) => 7 + i * 14;

describe("CommitGraph merge edges", () => {
  // マージの第2親が既に別レーンで待たれている形（親に複数の子がいて、
  // 子の片方が先に描画済み）。レイアウトはレーンを増やさないが、
  // レンダラはノード→既存レーンへの合流線を描かなければならない。
  it("draws a join edge to a pass-through lane that is also a parent", () => {
    const commits = [
      c("b88cf42", ["03e1d78"]),
      c("0377c44", ["6eb9b35", "03e1d78"]), // merge; 2nd parent already active in lane0
      c("03e1d78", ["18d0e57"]),
      c("18d0e57", []),
    ];
    const html = renderToStaticMarkup(<CommitGraph commits={commits} onSelect={() => {}} />);
    const rows = html.split("<li").slice(1);
    const mergeRow = rows[1]; // 0377c44 — node sits in lane1

    // ノード(lane1, 中央)→lane0 下端への合流線
    expect(mergeRow).toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(0)}" y2="48"`);
    // lane0 の素通り直線も残る（b88cf42→03e1d78 の継続）
    expect(mergeRow).toContain(`x1="${laneX(0)}" y1="24" x2="${laneX(0)}" y2="48"`);
  });

  // 親が独自レーンを取る通常のマージ（回帰確認）: ノードから扇形に出る線。
  it("still fans out to a freshly assigned parent lane", () => {
    const commits = [
      c("m1", ["p1", "p2"]),
      c("p1", []),
      c("p2", []),
    ];
    const html = renderToStaticMarkup(<CommitGraph commits={commits} onSelect={() => {}} />);
    const mergeRow = html.split("<li").slice(1)[0]; // m1 @lane0, p2 → lane1
    expect(mergeRow).toContain(`x1="${laneX(0)}" y1="24" x2="${laneX(1)}" y2="48"`);
  });
});
