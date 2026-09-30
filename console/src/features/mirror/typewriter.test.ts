// The typewriter reveal (#1274) as a pure rule: what a poll's text does to the state, and how far
// a frame of dt milliseconds gets. The invariant every case leans on is that the text on screen is
// always a prefix of the last poll's text — nothing invented, dropped or reordered.
import { describe, expect, it } from "vitest";
import {
  advance,
  caughtUp,
  cutAt,
  retarget,
  revealed,
  TYPEWRITER_CAP_CPS,
  TYPEWRITER_FLOOR_CPS,
  TYPEWRITER_IDLE,
  TYPEWRITER_MAX_LAG_MS,
  type Typewriter,
} from "./typewriter.ts";

// Type until caught up, in fixed frames, collecting what each frame shows.
function typeOut(t: Typewriter, frameMs: number, limit = 10_000): string[] {
  const frames: string[] = [];
  for (let i = 0; i < limit && !caughtUp(t); i++) {
    t = advance(t, frameMs);
    frames.push(revealed(t));
  }
  return frames;
}

describe("retarget", () => {
  it("starts a fresh block from nothing and paces it to land in about a second", () => {
    const t = retarget(TYPEWRITER_IDLE, "hello world");
    expect(revealed(t)).toBe("");
    expect(t.cps).toBe(TYPEWRITER_FLOOR_CPS); // 11 chars/s is below the floor
    const t2 = retarget(TYPEWRITER_IDLE, "x".repeat(200));
    expect(t2.cps).toBe(200);
  });

  it("keeps its place when the new text extends the old", () => {
    let t = retarget(TYPEWRITER_IDLE, "first line\n");
    t = advance(t, 100); // 4 chars at the floor
    expect(revealed(t)).toBe("firs");
    t = retarget(t, "first line\nsecond line\n");
    expect(revealed(t)).toBe("firs");
    expect(t.cps).toBeGreaterThan(0);
  });

  it("keeps typing from what is on screen when only the unseen part was rewritten", () => {
    let t = retarget(TYPEWRITER_IDLE, "abcdef");
    t = advance(t, 50);
    expect(revealed(t)).toBe("ab");
    const rewritten = retarget(t, "abXYZ");
    expect(revealed(rewritten)).toBe("ab");
    expect(revealed(advance(rewritten, 10_000))).toBe("abXYZ");
    const shorter = retarget(t, "abc"); // shrank, but not into what is shown
    expect(revealed(shorter)).toBe("ab");
  });

  it("shows a text that does not begin with what is on screen at once", () => {
    let t = retarget(TYPEWRITER_IDLE, "abcdef");
    t = advance(t, 50);
    expect(revealed(t)).toBe("ab");
    expect(revealed(retarget(t, "XYZ"))).toBe("XYZ"); // a different message
    expect(revealed(retarget(t, "a"))).toBe("a"); // shorter than what is shown
    expect(revealed(retarget(t, ""))).toBe("");
  });

  it("returns the same state for the same text", () => {
    const t = retarget(TYPEWRITER_IDLE, "same");
    expect(retarget(t, "same")).toBe(t);
  });

  it("caps the rate, and jumps ahead so the screen is never more than the lag budget behind", () => {
    const maxLag = (TYPEWRITER_CAP_CPS * TYPEWRITER_MAX_LAG_MS) / 1000;
    const long = "y".repeat(5000);
    const t = retarget(TYPEWRITER_IDLE, long);
    expect(t.cps).toBe(TYPEWRITER_CAP_CPS);
    expect(revealed(t)).toHaveLength(5000 - maxLag);
    // A moderate backlog is paced, not jumped.
    const mid = retarget(TYPEWRITER_IDLE, "z".repeat(600));
    expect(revealed(mid)).toBe("");
    expect(mid.cps).toBe(TYPEWRITER_CAP_CPS);
  });

  it("lands the jump where a cut may land: not inside a surrogate pair or a table row", () => {
    const maxLag = (TYPEWRITER_CAP_CPS * TYPEWRITER_MAX_LAG_MS) / 1000;
    const emoji = "a😀" + "x".repeat(maxLag - 1); // the jump would land between the halves of 😀
    expect(revealed(retarget(TYPEWRITER_IDLE, emoji))).toBe("a😀");
    const table = "| a | b |\n" + "y".repeat(maxLag - 4); // the jump would land inside the row
    expect(revealed(retarget(TYPEWRITER_IDLE, table))).toBe("| a | b |");
  });
});

describe("advance", () => {
  it("reveals a prefix every frame, in order, and ends on the whole text", () => {
    const text = "The quick brown fox jumps over the lazy dog, twice: the quick brown fox jumps over the lazy dog.";
    const frames = typeOut(retarget(TYPEWRITER_IDLE, text), 33);
    expect(frames.length).toBeGreaterThan(5);
    for (let i = 1; i < frames.length; i++) {
      expect(text.startsWith(frames[i])).toBe(true);
      expect(frames[i].length).toBeGreaterThanOrEqual(frames[i - 1].length);
    }
    expect(frames[frames.length - 1]).toBe(text);
  });

  it("finishes in about the catch-up window regardless of size", () => {
    for (const n of [100, 250, 400]) {
      const frames = typeOut(retarget(TYPEWRITER_IDLE, "a".repeat(n)), 33);
      const ms = frames.length * 33;
      expect(ms).toBeGreaterThan(900);
      expect(ms).toBeLessThan(1100);
    }
  });

  it("carries fractions of a character across frames instead of rounding them away", () => {
    // 40 chars (the floor rate) at 33 ms is 1.32 chars a frame: 25 frames must show about 33
    // chars, not 25.
    let t = retarget(TYPEWRITER_IDLE, "b".repeat(40));
    expect(t.cps).toBe(TYPEWRITER_FLOOR_CPS);
    for (let i = 0; i < 25; i++) t = advance(t, 33);
    expect(revealed(t).length).toBeGreaterThanOrEqual(32);
    expect(revealed(t).length).toBeLessThanOrEqual(33);
  });

  it("does nothing for a frame of no time, and is the same object once caught up", () => {
    const t = retarget(TYPEWRITER_IDLE, "abc");
    expect(advance(t, 0)).toBe(t);
    const done = advance(t, 10_000);
    expect(caughtUp(done)).toBe(true);
    expect(advance(done, 33)).toBe(done);
  });

  it("catches up in one frame after a long pause, as when the tab was in the background", () => {
    const t = retarget(TYPEWRITER_IDLE, "c".repeat(300));
    expect(revealed(advance(t, 60_000))).toBe("c".repeat(300));
  });
});

describe("cutAt", () => {
  it("never splits a surrogate pair", () => {
    const text = "ab😀cd";
    expect(cutAt(text, 3)).toBe(4); // between the halves of 😀
    expect(cutAt(text, 2)).toBe(2);
    expect(cutAt(text, 4)).toBe(4);
  });

  it("reveals a table row whole, so it never renders as a paragraph first", () => {
    const text = "intro\n| a | b |\n|---|---|\n| 1 | 2 |\ntail";
    expect(cutAt(text, 2)).toBe(2); // prose: cut anywhere
    expect(cutAt(text, 8)).toBe(text.indexOf("\n", 8)); // inside "| a | b |"
    expect(cutAt(text, 6)).toBe(text.indexOf("\n", 6)); // at the row's first character
    const lastRow = text.indexOf("| 1");
    expect(cutAt(text, lastRow + 2)).toBe(text.indexOf("\n", lastRow));
    expect(cutAt("  | indented |", 4)).toBe("  | indented |".length);
    expect(cutAt(text, text.length + 5)).toBe(text.length);
  });

  it("treats a GFM table without the leading bar as rows too", () => {
    const text = "a | b\n--- | ---\n1 | 2\nafter";
    expect(cutAt(text, 1)).toBe(text.indexOf("\n"));
    expect(cutAt(text, text.indexOf("---") + 1)).toBe(text.indexOf("\n", text.indexOf("---")));
    const after = text.indexOf("after");
    expect(cutAt(text, after + 2)).toBe(after + 2); // prose again
  });

  it("is what advance cuts with", () => {
    const text = "| a | b |\n|---|---|\n| 1 | 2 |";
    const frames = typeOut(retarget(TYPEWRITER_IDLE, text), 33);
    for (const f of frames) expect(f === "" || f.endsWith("|") || f.endsWith("\n")).toBe(true);
  });
});
