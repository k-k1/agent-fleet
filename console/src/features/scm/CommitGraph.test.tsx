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

// 行ごとの HTML に切り出す。区切りは "<li " と末尾スペース必須 — "<li" だけだと
// 中身の <line> にも当たって行がバラバラに割れる。
const renderRows = (commits: GraphCommit[]): string[] =>
  renderToStaticMarkup(<CommitGraph commits={commits} onSelect={() => {}} />)
    .split("<li ")
    .slice(1);

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
    const mergeRow = renderRows(commits)[1]; // 0377c44 — node sits in lane1

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
    const mergeRow = renderRows(commits)[0]; // m1 @lane0, p2 → lane1
    expect(mergeRow).toContain(`x1="${laneX(0)}" y1="24" x2="${laneX(1)}" y2="48"`);
  });
});

// 分岐点（1つの親に複数の子）。子それぞれが親を待つレーンを持つため、どの子の行でも
// 「兄弟レーンも同じ親を待っている」状態になる。ここで兄弟レーンへ合流線を引いてしまうと、
// ノードから自レーンの線と兄弟レーン色の斜線が同時に出て（＝両方の色の2本）、次行で
// 引き戻される「>」状のノイズになる。線は自レーンを素直に下り、親の行で1度だけ集まる。
describe("CommitGraph branch points", () => {
  // b1 と b2 が同じ親 base を持つ二股。b1@lane0 / b2@lane1。
  const commits = [
    c("b1", ["base"]),
    c("b2", ["base"]),
    c("base", []),
  ];
  it("does not draw a redundant edge into a sibling lane awaiting the same parent", () => {
    const b2Row = renderRows(commits)[1]; // b2 @lane1; lane0 も base を待っている
    // 自レーン(lane1)を素直に下る線だけがある
    expect(b2Row).toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(1)}" y2="48"`);
    // lane0 へ斜めに合流する線は引かない（これが「両方の色の2本」の正体）
    expect(b2Row).not.toContain(`x1="${laneX(1)}" y1="24" x2="${laneX(0)}" y2="48"`);
  });

  it("converges the sibling lanes exactly once, at the shared parent's row", () => {
    const baseRow = renderRows(commits)[2]; // base @lane0: lane0/lane1 の両方が曲がり込む
    expect(baseRow).toContain(`x1="${laneX(0)}" y1="0" x2="${laneX(0)}" y2="24"`);
    expect(baseRow).toContain(`x1="${laneX(1)}" y1="0" x2="${laneX(0)}" y2="24"`);
  });

  it("emits one edge per parent — never one per lane holding it", () => {
    // 子が5つある分岐点（実リポジトリの a7d5f90 と同形）。各子の行から出る下向きの線は
    // 「自レーンを下る1本」だけで、兄弟レーンの数だけ斜線が増えたりしない。
    const many = [
      c("k1", ["root"]), c("k2", ["root"]), c("k3", ["root"]),
      c("k4", ["root"]), c("k5", ["root"]), c("root", []),
    ];
    const k5 = renderRows(many)[4]; // 最後の子: lane0〜3 も root 待ち
    // ノード(lane4)を起点に下へ出る線だけを見る。lane0〜3 を素通りする兄弟の直線
    // (x1===x2) は別物で、これは残って当然。
    const fromNode = [...k5.matchAll(new RegExp(`x1="${laneX(4)}" y1="24" x2="(\\d+)" y2="48"`, "g"))];
    expect(fromNode.map((m) => m[1])).toEqual([String(laneX(4))]); // 自レーンへの1本のみ
  });
});
