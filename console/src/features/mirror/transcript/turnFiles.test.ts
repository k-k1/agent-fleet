// ターン末尾のファイルチップ（docs/log/68 P1）の畳み方。
import { describe, it, expect } from "vitest";
import { chipPart, turnFiles } from "./turnFiles.ts";
import type { Part } from "./types.ts";

const tool = (over: Partial<Part>): Part => ({ kind: "tool", tool: "Edit", ...over });

describe("turnFiles", () => {
  it("編集していないパートは拾わない", () => {
    expect(
      turnFiles([
        { kind: "text", text: "やります" },
        tool({ tool: "Bash", info: "ls" }),
        tool({ tool: "Read", file: "" }),
      ]),
    ).toEqual([]);
  });

  it("同じファイルへの複数回の編集を 1 つに畳み、差分は連結する", () => {
    const got = turnFiles([
      tool({ file: "/r/a.ts", edits: [{ old: "1", new: "2" }] }),
      tool({ file: "/r/b.ts", edits: [{ old: "x", new: "y" }] }),
      tool({ file: "/r/a.ts", edits: [{ old: "3", new: "4" }] }),
    ]);
    expect(got.map((f) => f.name)).toEqual(["a.ts", "b.ts"]); // 最初に触った順
    expect(got[0].edits).toHaveLength(2);
  });

  it("差分本体が無くても座標としては拾う（削除・差分を運ばない kind）", () => {
    const got = turnFiles([tool({ tool: "apply_patch", file: "/r/gone.ts", verb: "delete" })]);
    expect(got).toHaveLength(1);
    expect(got[0].verb).toBe("delete");
    expect(got[0].edits).toEqual([]);
  });

  it("最後の呼び出しが今のそのファイル（書いてから消したら削除）", () => {
    const got = turnFiles([
      tool({ file: "/r/a.ts", verb: "add", edits: [{ old: "", new: "x" }] }),
      tool({ file: "/r/a.ts", verb: "delete" }),
    ]);
    expect(got[0].verb).toBe("delete");
  });

  it("チップが差分ペインへ渡す形", () => {
    const [f] = turnFiles([tool({ tool: "Write", file: "/r/a.ts", edits: [{ old: "", new: "x" }] })]);
    expect(chipPart(f)).toEqual({ kind: "tool", tool: "Write", file: "/r/a.ts", edits: [{ old: "", new: "x" }] });
  });
});
