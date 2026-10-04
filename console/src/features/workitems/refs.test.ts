// The gates that decide whether a ticket-shaped token in prose becomes a link (#1659). The false
// positives listed here are the reason each gate exists.
import { describe, expect, it } from "vitest";
import { classifyWorkItemRef, originOf, resolveWorkItemRef, WORK_ITEM_HINT_RE, type WorkItemRefContext } from "./refs.ts";
import type { WorkItem } from "./read.ts";

const row = (provider: string, key: string, extra: Partial<WorkItem> = {}): WorkItem => ({
  id: `${provider}:${key}`,
  queryId: "q1",
  provider,
  kind: "issue",
  key,
  title: `title of ${key}`,
  state: "open",
  url: "",
  assignee: "",
  labels: [],
  labelColors: {},
  repo: "",
  updatedAt: "2026-10-01T00:00:00Z",
  checks: { state: "", total: 0, failed: 0, pending: 0 },
  mergeable: "",
  ...extra,
});

const ctx = (over: Partial<WorkItemRefContext> = {}): WorkItemRefContext => ({
  origin: { provider: "github", path: "octo/fleet" },
  items: [],
  known: new Map(),
  ...over,
});

describe("classifyWorkItemRef", () => {
  it("rewrites a bare number into the inbox key of the context repository", () => {
    expect(classifyWorkItemRef("#956", ctx())).toEqual({ provider: "github", key: "octo/fleet#956" });
  });

  it("leaves a bare number alone without a context repository", () => {
    expect(classifyWorkItemRef("#956", ctx({ origin: null }))).toBeNull();
  });

  it("does not read a colour-shaped number as an issue", () => {
    expect(classifyWorkItemRef("#112233", ctx())).toBeNull();
    expect(classifyWorkItemRef("#11223344", ctx())).toBeNull();
    expect(classifyWorkItemRef("#00112233", ctx())).toBeNull();
    expect(classifyWorkItemRef("#0", ctx())).toBeNull();
    expect(classifyWorkItemRef("#1234567", ctx())).toEqual({ provider: "github", key: "octo/fleet#1234567" });
    // …but the qualified form is unambiguous at any length.
    expect(classifyWorkItemRef("octo/fleet#112233", ctx())).toEqual({ provider: "github", key: "octo/fleet#112233" });
    expect(classifyWorkItemRef("octo/fleet#11223344", ctx())).toEqual({ provider: "github", key: "octo/fleet#11223344" });
  });

  it("reads a bare number on the context repository's own host, whatever the other host has cached", () => {
    // The same owner/name on both hosts is two different repositories.
    const ghRow = row("github", "team/app#7");
    const bbRow = row("bitbucket", "team/app#7");
    const bbOrigin = { provider: "bitbucket" as const, path: "team/app" };
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin, items: [ghRow] }))).toBeNull();
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin, items: [ghRow, bbRow] }))).toEqual({ provider: "bitbucket", key: "team/app#7" });
    const ghOrigin = { provider: "github" as const, path: "team/app" };
    expect(classifyWorkItemRef("#7", ctx({ origin: ghOrigin, items: [bbRow] }))).toEqual({ provider: "github", key: "team/app#7" });
  });

  it("takes the qualified form's provider from the cache, then the clones, then GitHub", () => {
    const items = [row("bitbucket", "team/app#7")];
    expect(classifyWorkItemRef("team/app#7", ctx({ items }))).toEqual({ provider: "bitbucket", key: "team/app#7" });
    const known = new Map([["team/svc", "bitbucket" as const]]);
    // A Bitbucket repository's uncached number is not linked (its #N is as often an issue).
    expect(classifyWorkItemRef("team/svc#3", ctx({ known }))).toBeNull();
    expect(classifyWorkItemRef("other/repo#3", ctx())).toEqual({ provider: "github", key: "other/repo#3" });
  });

  it("links a Bitbucket working copy's number only when the cache has it", () => {
    const origin = { provider: "bitbucket" as const, path: "team/app" };
    expect(classifyWorkItemRef("#7", ctx({ origin }))).toBeNull();
    expect(classifyWorkItemRef("#7", ctx({ origin, items: [row("bitbucket", "team/app#7")] }))).toEqual({
      provider: "bitbucket",
      key: "team/app#7",
    });
  });

  it("links a Jira key only for a project the cache knows", () => {
    const items = [row("jira", "G3M-5")];
    expect(classifyWorkItemRef("G3M-12", ctx({ items }))).toEqual({ provider: "jira", key: "G3M-12" });
    for (const tok of ["UTF-8", "SHA-256", "GPT-4", "P2-1", "ADR-0061"]) {
      expect(classifyWorkItemRef(tok, ctx({ items })), tok).toBeNull();
    }
  });
});

describe("WORK_ITEM_HINT_RE", () => {
  it("does not take C#, an HTML entity or a URL fragment for a reference", () => {
    for (const s of ["C# code", "&#123;", "page#12", "a/b/c#1"]) {
      expect(WORK_ITEM_HINT_RE.test(s), s).toBe(false);
    }
    for (const s of ["| #1649 |", "（#956）", "see octo/fleet#3", "PROJ-9 done"]) {
      expect(WORK_ITEM_HINT_RE.test(s), s).toBe(true);
    }
  });
});

describe("originOf", () => {
  it("accepts github.com and bitbucket.org only", () => {
    expect(originOf({ provider: "github", remote: "github.com", remotePath: "o/n" })).toEqual({ provider: "github", path: "o/n" });
    // A GHE host also badges as "github", but the inbox cannot reach it.
    expect(originOf({ provider: "github", remote: "github.corp.test", remotePath: "o/n" })).toBeNull();
    expect(originOf({ provider: "gitlab", remote: "gitlab.com", remotePath: "o/n" })).toBeNull();
    expect(originOf(undefined)).toBeNull();
  });
});

describe("resolveWorkItemRef", () => {
  it("returns the cached row when there is one", () => {
    const hit = row("github", "octo/fleet#956");
    expect(resolveWorkItemRef({ provider: "github", key: "octo/fleet#956" }, [hit])).toEqual({ item: hit, reference: false });
  });

  it("builds a stand-in with a tracker URL and nothing it does not know", () => {
    const { item, reference } = resolveWorkItemRef({ provider: "github", key: "octo/fleet#1649" }, []);
    expect(reference).toBe(true);
    expect(item).toMatchObject({ key: "octo/fleet#1649", repo: "octo/fleet", title: "", state: "" });
    expect(item.url).toBe("https://github.com/octo/fleet/issues/1649");
  });

  it("finds the Jira site through a cached row of the same tracker", () => {
    const items = [row("jira", "G3M-5", { url: "https://jira.example.test/browse/G3M-5" })];
    expect(resolveWorkItemRef({ provider: "jira", key: "G3M-12" }, items).item.url).toBe("https://jira.example.test/browse/G3M-12");
  });
});
