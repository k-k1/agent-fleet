import { describe, it, expect } from "vitest";
import { diffRows, splitDiffFiles } from "./GitDiff.tsx";

// git diff output ends with "\n", and the Agent passes it through byte for byte.
const oneFile = [
  "diff --git a/a.txt b/a.txt",
  "index 1111111..2222222 100644",
  "--- a/a.txt",
  "+++ b/a.txt",
  "@@ -6,2 +10,2 @@",
  " keep",
  "-old",
  "+new",
  "",
].join("\n");

const rowsOf = (text: string) => splitDiffFiles(text).map((f) => diffRows(f.lines));

describe("splitDiffFiles / diffRows", () => {
  it("renders no row for the newline that terminates the diff", () => {
    const [rows] = rowsOf(oneFile);
    expect(rows.map((r) => r.type)).toEqual(["hunk", "ctx", "del", "add"]);
    expect(rows[rows.length - 1]).toMatchObject({ type: "add", newLn: 11, text: "+new" });
  });

  it("adds no row after the last file of a multi-file diff, and none between files", () => {
    const text = oneFile + oneFile.replace(/a\.txt/g, "b.txt");
    const files = splitDiffFiles(text);
    expect(files.map((f) => f.path)).toEqual(["a.txt", "b.txt"]);
    for (const rows of files.map((f) => diffRows(f.lines))) {
      expect(rows.map((r) => r.type)).toEqual(["hunk", "ctx", "del", "add"]);
    }
  });

  it("keeps the no-newline marker as the last row, unnumbered", () => {
    const text = oneFile + "\\ No newline at end of file\n";
    const [rows] = rowsOf(text);
    expect(rows.map((r) => r.type)).toEqual(["hunk", "ctx", "del", "add", "meta"]);
    const last = rows[rows.length - 1];
    expect(last.text).toBe("\\ No newline at end of file");
    expect(last.oldLn).toBeUndefined();
    expect(last.newLn).toBeUndefined();
  });

  it("still counts an empty context line inside a hunk", () => {
    const text = ["@@ -1,3 +1,3 @@", " a", "", "+b", ""].join("\n");
    const [rows] = rowsOf(text);
    expect(rows.map((r) => r.type)).toEqual(["hunk", "ctx", "ctx", "add"]);
    expect(rows[3]).toMatchObject({ newLn: 3 });
  });

  it("drops only one trailing newline", () => {
    const text = ["@@ -1,2 +1,2 @@", " a", "", ""].join("\n");
    const [rows] = rowsOf(text);
    expect(rows.map((r) => r.type)).toEqual(["hunk", "ctx", "ctx"]);
  });
});
