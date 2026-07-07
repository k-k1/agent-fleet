import { describe, it, expect } from "vitest";
import type { Layout, OpenTarget } from "./types.ts";
import { blankPane } from "./types.ts";
import {
  freshLayout,
  allPanes,
  isBlankPane,
  openActive,
  openInNew,
  closePane,
  closeSessionPanes,
  swapPanes,
  dropSplit,
  splitRight,
  splitDown,
  setActive,
  MAX_COLS,
} from "./ops.ts";
import { normalizeStored } from "./migrate.ts";

const term = (session: string): OpenTarget => ({
  content: { kind: "terminal", chat: false },
  session,
});
const file = (filePath: string): OpenTarget => ({ content: { kind: "file", filePath } });

/** Grow a layout with n extra panes opened via openInNew (desktop). */
function grown(n: number): Layout {
  let l = openActive(freshLayout(), term("s0"));
  for (let i = 1; i <= n; i++) l = openInNew(l, term(`s${i}`), { force: true });
  return l;
}

describe("openActive", () => {
  it("binds a session to the active pane", () => {
    const l = openActive(freshLayout(), term("s1"));
    expect(allPanes(l)).toHaveLength(1);
    expect(allPanes(l)[0].session).toBe("s1");
    expect(allPanes(l)[0].content).toEqual({ kind: "terminal", chat: false });
  });
  it("keeps the pane's session when the target has none (back-to-terminal)", () => {
    let l = openActive(freshLayout(), term("s1"));
    l = openActive(l, file("a.md"));
    expect(allPanes(l)[0].content.kind).toBe("file");
    expect(allPanes(l)[0].session).toBe("s1"); // hidden terminal stays bound
    l = openActive(l, { content: { kind: "terminal", chat: false } });
    expect(allPanes(l)[0].session).toBe("s1"); // revealed, not re-bound
  });
  it("swaps payloads (keeping ids) when the other pane already shows the target", () => {
    let l = grown(1); // p0=s0 active would be the new pane; find ids
    const [a, b] = allPanes(l);
    l = setActive(l, a.id);
    const l2 = openActive(l, term(b.session!));
    const [a2, b2] = allPanes(l2);
    expect(a2.id).toBe(a.id); // ids stay in place (paneId contract)
    expect(b2.id).toBe(b.id);
    expect(a2.session).toBe(b.session); // payloads swapped
    expect(b2.session).toBe(a.session);
    expect(l2.activeId).toBe(a.id);
  });
});

describe("openInNew", () => {
  it("focuses an existing pane showing the target instead of duplicating", () => {
    const l = grown(1);
    const first = allPanes(l)[0];
    const l2 = openInNew(l, term(first.session!));
    expect(l2.cols).toBe(l.cols); // no structural change
    expect(l2.activeId).toBe(first.id);
  });
  it("fills a blank pane before growing the layout", () => {
    let l = grown(1);
    const second = allPanes(l)[1];
    l = closePane(l, second.id); // step 1: blanks it in place
    expect(isBlankPane(allPanes(l)[1])).toBe(true);
    const l2 = openInNew(l, term("s9"));
    expect(allPanes(l2)).toHaveLength(2); // reused, not grown
    expect(allPanes(l2)[1].session).toBe("s9");
  });
  it("adds right columns up to MAX_COLS, then splits columns down to 8 panes, then overwrites the last", () => {
    let l = grown(0);
    for (let i = 1; i < MAX_COLS; i++) l = openInNew(l, term(`c${i}`), { force: true });
    expect(l.cols).toHaveLength(MAX_COLS);
    for (let i = 0; i < MAX_COLS; i++) l = openInNew(l, term(`r${i}`), { force: true });
    expect(allPanes(l)).toHaveLength(8);
    expect(l.cols.every((c) => c.panes.length === 2)).toBe(true);
    const before = allPanes(l).map((p) => p.id);
    l = openInNew(l, term("overflow"), { force: true });
    expect(allPanes(l).map((p) => p.id)).toEqual(before); // no new ids
    const lastCol = l.cols[l.cols.length - 1];
    expect(lastCol.panes[1].session).toBe("overflow");
  });
  it("mobile: splits the active column down instead of adding a column", () => {
    let l = openActive(freshLayout(), term("s0"));
    l = openInNew(l, term("s1"), { mobile: true });
    expect(l.cols).toHaveLength(1);
    expect(l.cols[0].panes).toHaveLength(2);
    // Third open: column full → falls back to openActive on the active pane.
    const l2 = openInNew(l, term("s2"), { mobile: true });
    expect(allPanes(l2)).toHaveLength(2);
  });
  it("allocates ids past history maxima (no reuse after close)", () => {
    let l = grown(2); // p0,p1,p2
    l = closePane(l, "p1", true);
    l = openInNew(l, term("s9"), { force: true });
    expect(allPanes(l).some((p) => p.id === "p3")).toBe(true); // p1 not reused… p2 is max → p3
  });
});

describe("closePane", () => {
  it("blanks a content pane first, removes it on the second close", () => {
    let l = grown(1);
    const second = allPanes(l)[1];
    l = closePane(l, second.id);
    expect(allPanes(l)).toHaveLength(2); // split kept
    expect(isBlankPane(allPanes(l)[1])).toBe(true);
    l = closePane(l, second.id);
    expect(allPanes(l)).toHaveLength(1); // now removed
    expect(l.colRatios).toEqual([1]);
  });
  it("removeOutright removes a content pane at once", () => {
    let l = grown(1);
    const second = allPanes(l)[1];
    l = closePane(l, second.id, true);
    expect(allPanes(l)).toHaveLength(1);
  });
  it("closing the last pane resets to a fresh blank layout", () => {
    let l = openActive(freshLayout(), term("s0"));
    l = closePane(l, "p0", true);
    expect(allPanes(l)).toHaveLength(1);
    expect(isBlankPane(allPanes(l)[0])).toBe(true);
  });
  it("re-picks the active pane when it was removed", () => {
    let l = grown(1);
    const [first, second] = allPanes(l);
    expect(l.activeId).toBe(second.id);
    l = closePane(l, second.id, true);
    expect(l.activeId).toBe(first.id);
  });
});

describe("closeSessionPanes", () => {
  it("removes every pane showing the session in one step", () => {
    let l = grown(2);
    // Bind pane 3 to the same session as pane 1 (force split then swap in s0).
    l = openActive(l, term("s0")); // active pane (third) now shows s0 → swap-dedup…
    // After the swap, exactly one pane shows s0. Bind another explicitly:
    l = openInNew(l, term("s0x"), { force: true });
    const before = allPanes(l).length;
    const hits = allPanes(l).filter((p) => p.session === "s0").length;
    const l2 = closeSessionPanes(l, "s0");
    expect(allPanes(l2)).toHaveLength(before - hits);
    expect(allPanes(l2).every((p) => p.session !== "s0")).toBe(true);
  });
  it("no-ops (same reference) when the session isn't shown", () => {
    const l = grown(1);
    expect(closeSessionPanes(l, "nope")).toBe(l);
  });
});

describe("swapPanes / dropSplit", () => {
  it("swap keeps ids in place and activates the drop target", () => {
    const l = grown(1);
    const [a, b] = allPanes(l);
    const l2 = swapPanes(l, a.id, b.id);
    const [a2, b2] = allPanes(l2);
    expect([a2.id, b2.id]).toEqual([a.id, b.id]);
    expect(a2.session).toBe(b.session);
    expect(l2.activeId).toBe(b.id);
  });
  it("dropSplit right moves the SAME pane id into a new column", () => {
    let l = grown(1); // 2 columns
    l = splitDown(l, allPanes(l)[0].id); // col0 now has 2 panes
    const src = l.cols[0].panes[1];
    const l2 = dropSplit(l, src.id, l.cols[1].panes[0].id, "right");
    expect(l2.cols).toHaveLength(3);
    const moved = l2.cols[2].panes[0];
    expect(moved.id).toBe(src.id); // id preserved (paneId contract)
    expect(moved.session).toBe(src.session);
    expect(l2.activeId).toBe(src.id);
  });
  it("dropSplit down collapses the origin column and stacks under the target", () => {
    const l = grown(1); // [p0][p1]
    const [a, b] = allPanes(l);
    const l2 = dropSplit(l, b.id, a.id, "down");
    expect(l2.cols).toHaveLength(1);
    expect(l2.cols[0].panes.map((p) => p.id)).toEqual([a.id, b.id]);
    expect(l2.colRatios).toEqual([1]);
  });
  it("dropSplit right no-ops when it would exceed MAX_COLS", () => {
    let l = grown(3); // 4 columns of 1
    l = splitDown(l, l.cols[0].panes[0].id); // col0 = 2 panes → 5 panes total
    const src = l.cols[0].panes[1];
    expect(dropSplit(l, src.id, l.cols[3].panes[0].id, "right")).toBe(l);
  });
});

describe("splitRight / splitDown", () => {
  it("splitRight caps at MAX_COLS", () => {
    let l = freshLayout();
    for (let i = 0; i < MAX_COLS + 2; i++) l = splitRight(l);
    expect(l.cols).toHaveLength(MAX_COLS);
  });
  it("splitDown caps at 2 rows", () => {
    let l = freshLayout();
    l = splitDown(l, "p0");
    expect(splitDown(l, "p0")).toBe(l);
  });
});

describe("normalizeStored (migration)", () => {
  it("migrates the old flat format, keeping ids, sessions and hidden terminals", () => {
    const old = {
      cols: [
        {
          id: "c0",
          rowRatio: 0.4,
          panes: [
            // Old wide struct: a file view whose pane still holds a warm session.
            { id: "p0", kind: "file", session: "s1", chat: false, filePath: "notes.md", scmRepo: null, commitSha: null, diffStaged: null, docTitle: null, docContent: null, diffTool: null, diffEdits: null, conversationId: null, draftAssistantId: null, wrap: true },
            { id: "p2", kind: "terminal", session: "s2", chat: true, filePath: null, scmRepo: null, commitSha: null, diffStaged: null, docTitle: null, docContent: null, diffTool: null, diffEdits: null, conversationId: null, draftAssistantId: null, wrap: null },
          ],
        },
        { id: "c1", rowRatio: 0.5, panes: [{ id: "p1", kind: "commit", session: null, scmRepo: "repo", commitSha: "abc123", wrap: null }] },
      ],
      colRatios: [0.6, 0.4],
      activeId: "p2",
    };
    const l = normalizeStored(old)!;
    expect(l.activeId).toBe("p2");
    expect(l.colRatios).toEqual([0.6, 0.4]);
    const [p0, p2, p1] = allPanes(l);
    expect(p0).toMatchObject({ id: "p0", session: "s1", wrap: true, content: { kind: "file", filePath: "notes.md" } });
    expect(p2).toMatchObject({ id: "p2", session: "s2", content: { kind: "terminal", chat: true } });
    expect(p1).toMatchObject({ id: "p1", session: null, content: { kind: "commit", scmRepo: "repo", commitSha: "abc123" } });
  });
  it("degrades a payload-less view pane to a blank terminal instead of failing", () => {
    const l = normalizeStored({
      cols: [{ id: "c0", panes: [{ id: "p0", kind: "file", filePath: null }] }],
      colRatios: [1],
      activeId: "p0",
    })!;
    expect(l.cols[0].panes[0].content).toEqual({ kind: "terminal", chat: false });
  });
  it("round-trips the new format", () => {
    const l = grown(1);
    const back = normalizeStored(JSON.parse(JSON.stringify(l)))!;
    expect(back).toEqual(l);
  });
  it("rejects garbage and repairs bad ratios / activeId", () => {
    expect(normalizeStored(null)).toBeNull();
    expect(normalizeStored({ cols: [] })).toBeNull();
    expect(normalizeStored({ cols: [{ id: "c0", panes: [{}] }] })).toBeNull(); // pane without id
    const l = normalizeStored({
      cols: [{ id: "c0", panes: [blankPane("p0")] }],
      colRatios: [0.3, 0.7], // wrong length
      activeId: "missing",
    })!;
    expect(l.colRatios).toEqual([1]);
    expect(l.activeId).toBe("p0");
  });
});
