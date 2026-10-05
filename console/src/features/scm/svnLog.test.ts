import { describe, expect, it } from "vitest";
import {
  actionSummary,
  changeTone,
  isSvnAuthError,
  mergeLogPage,
  messageSubject,
  normalizeFilterPath,
  oldestRev,
  revSide,
  sortChanges,
  splitRepoPath,
  statusKey,
  svnLogUrl,
} from "./svnLog.ts";
import type { SvnChange, SvnRevision } from "./svnLog.ts";

const rev = (n: number): SvnRevision => ({ rev: n, author: "a", date: "2026-10-01T00:00:00Z", message: "m" + n, paths: [] });

describe("svnLogUrl", () => {
  it("is bare with no options", () => {
    expect(svnLogUrl("my repo")).toBe("api/repos/my%20repo/svn-log");
  });
  it("carries limit, a normalized path and from", () => {
    expect(svnLogUrl("r", { limit: 50, path: "/sub dir/", from: 12 })).toBe("api/repos/r/svn-log?limit=50&path=sub%20dir&from=12");
  });
  it("leaves out an empty path and a zero from", () => {
    expect(svnLogUrl("r", { path: " ./ ", from: 0 })).toBe("api/repos/r/svn-log");
  });
});

describe("normalizeFilterPath", () => {
  it("trims and strips leading/trailing separators", () => {
    expect(normalizeFilterPath("  ./a/b/ ")).toBe("a/b");
    expect(normalizeFilterPath("/a\\b")).toBe("a/b");
    expect(normalizeFilterPath(".")).toBe("");
  });
});

describe("paging", () => {
  it("finds the oldest revision for the next page's from", () => {
    expect(oldestRev([rev(9), rev(7), rev(8)])).toBe(7);
    expect(oldestRev([])).toBeUndefined();
  });
  it("appends older revisions newest-first and drops duplicates", () => {
    const merged = mergeLogPage([rev(9), rev(8)], [rev(8), rev(7), rev(6)]);
    expect(merged.map((r) => r.rev)).toEqual([9, 8, 7, 6]);
  });
});

describe("revSide", () => {
  it("marks revisions against the working copy's revision", () => {
    expect(revSide(12, "10")).toBe("newer");
    expect(revSide(10, "10")).toBe("current");
    expect(revSide(9, "10")).toBe("older");
  });
  it("answers unknown when the working copy's revision is missing", () => {
    expect(revSide(5, "")).toBe("unknown");
    expect(revSide(5, undefined)).toBe("unknown");
    expect(revSide(5, "abc")).toBe("unknown");
  });
});

describe("small helpers", () => {
  it("takes the first line of a message", () => {
    expect(messageSubject("fix a\n\nbody")).toBe("fix a");
    expect(messageSubject("")).toBe("");
  });
  it("recognizes only the machine code as an auth error", () => {
    expect(isSvnAuthError({ error: { code: "svn_auth_required" } })).toBe(true);
    expect(isSvnAuthError({ error: { code: "svn_failed", message: "E170001 authorization failed" } })).toBe(false);
    expect(isSvnAuthError(null)).toBe(false);
  });
  it("summarises changed paths by action", () => {
    expect(actionSummary([{ action: "M", path: "/a" }, { action: "M", path: "/b" }, { action: "A", path: "/c" }])).toBe("1 A · 2 M");
  });
  it("splits a Files-tree path into working copy and inner path", () => {
    expect(splitRepoPath("repos/wc/sub/x.txt")).toEqual({ repo: "wc", sub: "sub/x.txt" });
    expect(splitRepoPath("repos/wc")).toEqual({ repo: "wc", sub: "" });
    expect(splitRepoPath("home/x")).toBeNull();
    expect(splitRepoPath("repos/")).toBeNull();
  });
});

describe("changes", () => {
  const c = (status: string, over: Partial<SvnChange> = {}): SvnChange => ({ path: "p", status, untracked: false, conflict: false, ...over });
  it("picks a tone per status", () => {
    expect(changeTone(c("M"))).toBe("unstaged");
    expect(changeTone(c("A"))).toBe("staged");
    expect(changeTone(c("?", { untracked: true }))).toBe("untracked");
    expect(changeTone(c("C", { conflict: true }))).toBe("conflict");
  });
  it("maps letters to i18n keys, unknown to modified", () => {
    expect(statusKey("?")).toBe("svn.status_unversioned");
    expect(statusKey("Z")).toBe("svn.status_modified");
  });
  it("lists conflicts first, then by path", () => {
    const sorted = sortChanges([c("M", { path: "b" }), c("M", { path: "a" }), c("C", { path: "z", conflict: true })]);
    expect(sorted.map((x) => x.path)).toEqual(["z", "a", "b"]);
  });
});
