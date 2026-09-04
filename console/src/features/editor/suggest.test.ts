// Pre-apply validation of AI suggestions and the context windows around them
// (docs/log/44 §4.2 / Phase 4).
import { describe, expect, it } from "vitest";
import { revisionOf } from "./buffer.ts";
import {
  checkSuggestion,
  suggestWindows,
  SUGGEST_MAX_CONTEXT_BYTES,
  SUGGEST_MAX_SELECTION_BYTES,
  type EditSuggestionEnvelope,
} from "./suggest.ts";

const content = "# title\nbody line\ntail\n";
const revision = revisionOf(content);
const ctx = { paneId: "p1", path: "repos/a.md", bufferRevision: revision, content };

function envelope(overrides: Partial<EditSuggestionEnvelope> = {}): EditSuggestionEnvelope {
  return {
    kind: "edit_suggestion",
    version: 1,
    paneId: "p1",
    filePath: "repos/a.md",
    requestId: "req-1",
    sourceRevision: revision,
    suggestion: {
      summary: "見出しを具体化",
      replacement: "# concrete title",
      range: { from: 0, to: 7 },
      baseRevision: revision,
    },
    ...overrides,
  };
}

describe("checkSuggestion (docs/log/44 §4.2)", () => {
  it("applies a valid suggestion to the exact range", () => {
    const result = checkSuggestion(envelope(), ctx);
    expect(result).toEqual({ ok: true, applied: "# concrete title\nbody line\ntail\n" });
  });

  it("rejects identity mismatches as suggestion_invalid", () => {
    for (const bad of [
      envelope({ paneId: "other" }),
      envelope({ filePath: "repos/b.md" }),
      envelope({ requestId: "" }),
      envelope({ kind: "something" as never }),
      envelope({ version: 2 as never }),
    ]) {
      expect(checkSuggestion(bad, ctx)).toEqual({ ok: false, code: "suggestion_invalid" });
    }
  });

  it("rejects a broken revision triple as suggestion_stale", () => {
    const other = revisionOf("other\n");
    const source = envelope({ sourceRevision: other });
    expect(checkSuggestion(source, ctx)).toEqual({ ok: false, code: "suggestion_stale" });
    const base = envelope();
    base.suggestion.baseRevision = other;
    expect(checkSuggestion(base, ctx)).toEqual({ ok: false, code: "suggestion_stale" });
    // Buffer moved on after receipt: it no longer matches the current revision.
    const moved = { ...ctx, content: content + "x", bufferRevision: revisionOf(content + "x") };
    expect(checkSuggestion(envelope(), moved)).toEqual({ ok: false, code: "suggestion_stale" });
  });

  it("rejects malformed ranges and surrogate splits as suggestion_invalid", () => {
    for (const range of [
      { from: -1, to: 3 },
      { from: 5, to: 4 },
      { from: 0, to: content.length + 1 },
      { from: 0.5, to: 3 },
    ]) {
      const bad = envelope();
      bad.suggestion.range = range;
      expect(checkSuggestion(bad, ctx)).toEqual({ ok: false, code: "suggestion_invalid" });
    }
    const emoji = "a😀b\n";
    const emojiCtx = { ...ctx, content: emoji, bufferRevision: revisionOf(emoji) };
    const split = envelope({ sourceRevision: emojiCtx.bufferRevision });
    split.suggestion.baseRevision = emojiCtx.bufferRevision;
    split.suggestion.range = { from: 2, to: 3 }; // ペアの内側
    expect(checkSuggestion(split, emojiCtx)).toEqual({ ok: false, code: "suggestion_invalid" });
  });

  it("rejects bad summaries and validator-violating replacements", () => {
    const empty = envelope();
    empty.suggestion.summary = "  ";
    expect(checkSuggestion(empty, ctx)).toEqual({ ok: false, code: "suggestion_invalid" });
    const long = envelope();
    long.suggestion.summary = "あ".repeat(81); // 243 bytes
    expect(checkSuggestion(long, ctx)).toEqual({ ok: false, code: "suggestion_invalid" });
    const cr = envelope();
    cr.suggestion.replacement = "a\r\nb";
    expect(checkSuggestion(cr, ctx)).toEqual({ ok: false, code: "unsupported_newline" });
    const huge = envelope();
    huge.suggestion.replacement = "a".repeat(2 * 1024 * 1024 + 1);
    expect(checkSuggestion(huge, ctx)).toEqual({ ok: false, code: "too_large" });
  });

  it("allows an empty replacement (deletion)", () => {
    const del = envelope();
    del.suggestion.replacement = "";
    expect(checkSuggestion(del, ctx)).toEqual({ ok: true, applied: "\nbody line\ntail\n" });
  });
});

describe("suggestWindows", () => {
  it("returns full windows for a small document", () => {
    expect(suggestWindows(content, { from: 8, to: 17 })).toEqual({
      before: "# title\n",
      selection: "body line",
      after: "\ntail\n",
    });
  });

  it("refuses an oversized selection instead of truncating it", () => {
    const big = "x".repeat(SUGGEST_MAX_SELECTION_BYTES + 1);
    expect(suggestWindows(big, { from: 0, to: big.length })).toBeNull();
  });

  it("clamps context windows to the byte cap on line boundaries", () => {
    const line = "0123456789\n";
    const doc = line.repeat(4000); // 44,000 bytes each side of the middle
    const middle = doc.length / 2;
    const windows = suggestWindows(doc, { from: middle, to: middle + line.length })!;
    const bytes = (s: string) => new TextEncoder().encode(s).byteLength;
    expect(bytes(windows.before)).toBeLessThanOrEqual(SUGGEST_MAX_CONTEXT_BYTES);
    expect(bytes(windows.after)).toBeLessThanOrEqual(SUGGEST_MAX_CONTEXT_BYTES);
    // A cut edge is rounded to a line boundary.
    expect(windows.before.startsWith("0")).toBe(true);
    expect(windows.before.endsWith("\n")).toBe(true);
    expect(windows.after.endsWith("\n")).toBe(true);
  });
});
