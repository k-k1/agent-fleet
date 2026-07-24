// Pop-out descriptor logic (layout/popout.ts) + the 1-pane seed (ops.singlePaneLayout).
import { describe, it, expect } from "vitest";
import {
  canPopout,
  encodePopoutDescriptor,
  parsePopoutDescriptor,
  isStalePopoutEntry,
  POPOUT_STALE_MS,
} from "./popout.ts";
import { singlePaneLayout, activePane, isBlankPane } from "./ops.ts";
import { normalizeStored } from "./migrate.ts";
import type { Pane, PaneContent } from "./types.ts";
import { blankPane } from "./types.ts";

const pane = (content: PaneContent, session: string | null = null, wrap: boolean | null = null): Pane => ({
  id: "p3",
  session,
  content,
  wrap,
});

const NOW = 1700000000000;

describe("canPopout", () => {
  it("rejects a blank terminal pane", () => {
    expect(canPopout(blankPane("p0"))).toBe(false);
  });
  it("accepts a session terminal / mirror pane", () => {
    expect(canPopout(pane({ kind: "terminal", chat: false }, "sess-a"))).toBe(true);
    expect(canPopout(pane({ kind: "terminal", chat: true }, "sess-a"))).toBe(true);
  });
  it("rejects a not-yet-created chat draft, accepts a real conversation", () => {
    expect(canPopout(pane({ kind: "chat", conversationId: null, draftAssistantId: "d1" }))).toBe(false);
    expect(canPopout(pane({ kind: "chat", conversationId: "c1", draftAssistantId: null }))).toBe(true);
  });
  it("accepts content panes (doc / file / scm / browser)", () => {
    expect(canPopout(pane({ kind: "doc", docTitle: "t", docContent: "x" }))).toBe(true);
    expect(canPopout(pane({ kind: "file", filePath: "/a/b.ts" }))).toBe(true);
    expect(canPopout(pane({ kind: "scm", scmRepo: "repo" }))).toBe(true);
    expect(canPopout(pane({ kind: "browser", port: 5173, path: "/" }))).toBe(true);
  });
});

describe("descriptor round-trip", () => {
  const KINDS: Array<[PaneContent, string | null]> = [
    [{ kind: "terminal", chat: true }, "sess-a"],
    [{ kind: "file", filePath: "/repo/src/x.ts", targetLine: 12 }, null],
    [{ kind: "read", filePath: "/repo/README.md" }, null],
    [{ kind: "scm", scmRepo: "repo", scmPath: "sub" }, null],
    [{ kind: "changes", scmRepo: "repo" }, null],
    [{ kind: "commit", scmRepo: "repo", commitSha: "abc1234" }, null],
    [{ kind: "wtdiff", scmRepo: "repo", filePath: "a.go", diffStaged: true }, null],
    [{ kind: "doc", docTitle: "レポート", docContent: "# 長い本文\n".repeat(1000) }, null],
    [{ kind: "chat", conversationId: "c1", draftAssistantId: null }, null],
    [{ kind: "browser", port: 5173, path: "/app" }, null],
  ];
  it.each(KINDS)("%j survives encode → parse", (content, session) => {
    const raw = encodePopoutDescriptor(pane(content, session, true), "popout", "acme", NOW);
    const d = parsePopoutDescriptor(raw);
    expect(d).not.toBeNull();
    expect(d!.content).toEqual(content);
    expect(d!.session).toBe(session);
    expect(d!.ui).toBe("popout");
    expect(d!.tenant).toBe("acme");
    expect(d!.wrap).toBe(true);
    expect(d!.ts).toBe(NOW);
  });
  it("omits the tenant field when none is selected", () => {
    const d = parsePopoutDescriptor(encodePopoutDescriptor(pane({ kind: "changes", scmRepo: "r" }), "full", "", NOW));
    expect(d!.tenant).toBeUndefined();
    expect(d!.ui).toBe("full");
  });
  it("rejects garbage, wrong version, and degraded-to-blank content", () => {
    expect(parsePopoutDescriptor(null)).toBeNull();
    expect(parsePopoutDescriptor("not json")).toBeNull();
    expect(parsePopoutDescriptor(JSON.stringify({ v: 2, ui: "popout", content: { kind: "changes", scmRepo: "r" } }))).toBeNull();
    expect(parsePopoutDescriptor(JSON.stringify({ v: 1, ui: "nope", content: { kind: "changes", scmRepo: "r" } }))).toBeNull();
    // file without a path degrades to a blank terminal → unusable
    expect(parsePopoutDescriptor(JSON.stringify({ v: 1, ui: "popout", content: { kind: "file" } }))).toBeNull();
  });
  it("keeps a bare session terminal (content degrades but session identifies it)", () => {
    const d = parsePopoutDescriptor(JSON.stringify({ v: 1, ui: "popout", session: "sess-a", content: { kind: "terminal", chat: false } }));
    expect(d).not.toBeNull();
    expect(d!.session).toBe("sess-a");
  });
});

describe("isStalePopoutEntry", () => {
  const fresh = encodePopoutDescriptor(pane({ kind: "changes", scmRepo: "r" }), "popout", "", NOW);
  it("fresh entries survive, old / unparsable ones are stale", () => {
    expect(isStalePopoutEntry(fresh, NOW + 1000)).toBe(false);
    expect(isStalePopoutEntry(fresh, NOW + POPOUT_STALE_MS + 1)).toBe(true);
    expect(isStalePopoutEntry("garbage", NOW)).toBe(true);
    expect(isStalePopoutEntry(null, NOW)).toBe(true);
  });
});

describe("singlePaneLayout", () => {
  it("builds a valid 1-pane layout carrying the descriptor", () => {
    const l = singlePaneLayout({ kind: "terminal", chat: true }, "sess-a", true);
    expect(l.cols).toHaveLength(1);
    expect(l.colRatios).toEqual([1]);
    const p = activePane(l)!;
    expect(p.id).toBe("p0");
    expect(p.session).toBe("sess-a");
    expect(p.content).toEqual({ kind: "terminal", chat: true });
    expect(p.wrap).toBe(true);
    expect(isBlankPane(p)).toBe(false);
    // Round-trips through the persistence validator unchanged (reload path).
    expect(normalizeStored(JSON.parse(JSON.stringify(l)))).toEqual(l);
  });
});
