// How the file chips at the end of a turn (docs/log/68 P1) are folded.
import { describe, it, expect } from "vitest";
import { chipPart, turnFiles } from "./turnFiles.ts";
import type { Part } from "./types.ts";

const tool = (over: Partial<Part>): Part => ({ kind: "tool", tool: "Edit", ...over });

describe("turnFiles", () => {
  it("ignores parts that edited nothing", () => {
    expect(
      turnFiles([
        { kind: "text", text: "やります" },
        tool({ tool: "Bash", info: "ls" }),
        tool({ tool: "Read", file: "" }),
      ]),
    ).toEqual([]);
  });

  it("folds repeated edits of one file into one chip, concatenating the diffs", () => {
    const got = turnFiles([
      tool({ file: "/r/a.ts", edits: [{ old: "1", new: "2" }] }),
      tool({ file: "/r/b.ts", edits: [{ old: "x", new: "y" }] }),
      tool({ file: "/r/a.ts", edits: [{ old: "3", new: "4" }] }),
    ]);
    expect(got.map((f) => f.name)).toEqual(["a.ts", "b.ts"]); // order of first touch
    expect(got[0].edits).toHaveLength(2);
  });

  it("still records a file with no diff body (a delete, or a kind that carries no diffs)", () => {
    const got = turnFiles([tool({ tool: "apply_patch", file: "/r/gone.ts", verb: "delete" })]);
    expect(got).toHaveLength(1);
    expect(got[0].verb).toBe("delete");
    expect(got[0].edits).toEqual([]);
  });

  it("takes the last call as what the file is now (written then deleted = deleted)", () => {
    const got = turnFiles([
      tool({ file: "/r/a.ts", verb: "add", edits: [{ old: "", new: "x" }] }),
      tool({ file: "/r/a.ts", verb: "delete" }),
    ]);
    expect(got[0].verb).toBe("delete");
  });

  it("shapes what a chip hands to the diff pane", () => {
    const [f] = turnFiles([tool({ tool: "Write", file: "/r/a.ts", edits: [{ old: "", new: "x" }] })]);
    expect(chipPart(f)).toEqual({ kind: "tool", tool: "Write", file: "/r/a.ts", edits: [{ old: "", new: "x" }] });
  });
});
