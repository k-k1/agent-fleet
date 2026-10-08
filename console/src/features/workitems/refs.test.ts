// The gates that decide whether a ticket-shaped token in prose becomes a link (#1659). The false
// positives listed here are the reason each gate exists.
import { describe, expect, it } from "vitest";
import { classifyWorkItemRef, cloneHosts, originOf, resolveWorkItemRef, WORK_ITEM_HINT_RE, workItemRefInputs, type WorkItemRefContext } from "./refs.ts";
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
const gh = (n: string) => row("github", `octo/fleet#${n}`);
const code = true;

describe("classifyWorkItemRef", () => {
  it("links any GitHub number in prose, merged and closed ones included", () => {
    expect(classifyWorkItemRef("#956", ctx({ items: [gh("956")] }))).toEqual({ provider: "github", key: "octo/fleet#956" });
    // Not in the inbox (which holds open items only): still linked, as a guess.
    expect(classifyWorkItemRef("#1649", ctx())).toEqual({ provider: "github", key: "octo/fleet#1649", guessed: true });
    expect(classifyWorkItemRef("octo/other#3", ctx())).toEqual({ provider: "github", key: "octo/other#3", guessed: true });
  });

  it("links only a ticket the inbox holds in inline code (#166 typed as a search is not a citation)", () => {
    expect(classifyWorkItemRef("#166", ctx({ items: [gh("956")] }), code)).toBeNull();
    expect(classifyWorkItemRef("octo/fleet#166", ctx({ items: [gh("956")] }), code)).toBeNull();
    expect(classifyWorkItemRef("#956", ctx({ items: [gh("956")] }), code)).toEqual({ provider: "github", key: "octo/fleet#956" });
  });

  it("leaves a bare number alone without a context repository", () => {
    expect(classifyWorkItemRef("#956", ctx({ origin: null, items: [gh("956")] }))).toBeNull();
    // The qualified form needs none.
    expect(classifyWorkItemRef("octo/fleet#956", ctx({ origin: null, items: [gh("956")] }))).toEqual({
      provider: "github",
      key: "octo/fleet#956",
    });
  });

  it("does not read a colour-shaped number as an issue, even a cached one", () => {
    const items = [gh("112233"), gh("11223344")];
    expect(classifyWorkItemRef("#112233", ctx({ items }))).toBeNull();
    expect(classifyWorkItemRef("#11223344", ctx({ items }))).toBeNull();
    expect(classifyWorkItemRef("#00112233", ctx({ items }))).toBeNull();
    expect(classifyWorkItemRef("#0", ctx({ items }))).toBeNull();
    expect(classifyWorkItemRef("#1234567", ctx())?.key).toBe("octo/fleet#1234567");
    // …but the qualified form is unambiguous at any length.
    expect(classifyWorkItemRef("octo/fleet#11223344", ctx({ items }))).toEqual({ provider: "github", key: "octo/fleet#11223344" });
  });

  it("never guesses Bitbucket: a bare or qualified number there links only when cached", () => {
    const bbOrigin = { provider: "bitbucket" as const, path: "team/app" };
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin }))).toBeNull();
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin, items: [row("bitbucket", "team/app#7")] }))).toEqual({
      provider: "bitbucket",
      key: "team/app#7",
    });
    const known = cloneHosts([{ provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/svc" }]);
    expect(classifyWorkItemRef("team/svc#3", ctx({ known }))).toBeNull();
  });

  it("guesses an uncached qualified number on its own repository's host, whatever order the clones are in", () => {
    const bb = { name: "bb-copy", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/both" };
    const ghc = { name: "gh-copy", provider: "github", remote: "github.com", remotePath: "team/both" };
    for (const repos of [[bb, ghc], [ghc, bb]]) {
      const known = cloneHosts(repos);
      // Written from the Bitbucket copy: its own repository, so never guessed onto GitHub.
      expect(classifyWorkItemRef("team/both#7", ctx({ origin: { provider: "bitbucket", path: "team/both" }, known }))).toBeNull();
      // Written from the GitHub copy: a guess on GitHub.
      expect(classifyWorkItemRef("team/both#7", ctx({ origin: { provider: "github", path: "team/both" }, known }))?.provider).toBe("github");
      // From elsewhere: cloned from both hosts, so the host is unknown and nothing is guessed.
      expect(classifyWorkItemRef("team/both#7", ctx({ known }))).toBeNull();
    }
  });

  it("reads a bare number on the context repository's own host, whatever the other host has cached", () => {
    // The same owner/name on both hosts is two different repositories.
    const ghRow = row("github", "team/app#7");
    const bbRow = row("bitbucket", "team/app#7");
    const bbOrigin = { provider: "bitbucket" as const, path: "team/app" };
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin, items: [ghRow] }))).toBeNull();
    expect(classifyWorkItemRef("#7", ctx({ origin: bbOrigin, items: [ghRow, bbRow] }))).toEqual({ provider: "bitbucket", key: "team/app#7" });
    const ghOrigin = { provider: "github" as const, path: "team/app" };
    // Not GitHub's cached row: a guess on the origin's own host.
    expect(classifyWorkItemRef("#7", ctx({ origin: ghOrigin, items: [bbRow] }))).toEqual({ provider: "github", key: "team/app#7", guessed: true });
  });

  it("takes the qualified form's host from the cached row, the context's host first", () => {
    expect(classifyWorkItemRef("team/app#7", ctx({ items: [row("bitbucket", "team/app#7")] }))).toEqual({
      provider: "bitbucket",
      key: "team/app#7",
    });
    const both = [row("github", "team/app#7"), row("bitbucket", "team/app#7")];
    const bbOrigin = { provider: "bitbucket" as const, path: "team/app" };
    expect(classifyWorkItemRef("team/app#7", ctx({ origin: bbOrigin, items: both }))?.provider).toBe("bitbucket");
    expect(classifyWorkItemRef("team/app#7", ctx({ items: both }))?.provider).toBe("github");
    // The text's own host first even for another repository: a Bitbucket session cites Bitbucket.
    const other = [row("github", "team/other#7"), row("bitbucket", "team/other#7")];
    expect(classifyWorkItemRef("team/other#7", ctx({ origin: bbOrigin, items: other }))?.provider).toBe("bitbucket");
  });

  it("links a Jira key for a project the inbox holds in prose, and only a held issue in code", () => {
    const items = [row("jira", "G3M-5")];
    expect(classifyWorkItemRef("G3M-5", ctx({ items }), code)).toEqual({ provider: "jira", key: "G3M-5" });
    expect(classifyWorkItemRef("G3M-12", ctx({ items }))).toEqual({ provider: "jira", key: "G3M-12", guessed: true });
    expect(classifyWorkItemRef("G3M-12", ctx({ items }), code)).toBeNull();
    for (const tok of ["UTF-8", "SHA-256", "GPT-4", "P2-1", "ADR-0061"]) {
      expect(classifyWorkItemRef(tok, ctx({ items })), tok).toBeNull();
    }
  });

  it("links a key of a project on the member's Jira connection without a cached row (#1899)", () => {
    const jiraProjects = new Set(["OPS", "G3M"]);
    expect(classifyWorkItemRef("OPS-7", ctx({ jiraProjects }))).toEqual({ provider: "jira", key: "OPS-7", guessed: true });
    // Prose only, as for a cached project; and only the projects the connection lists.
    expect(classifyWorkItemRef("OPS-7", ctx({ jiraProjects }), code)).toBeNull();
    for (const tok of ["UTF-8", "SHA-256", "ADR-0061", "WEB-1"]) {
      expect(classifyWorkItemRef(tok, ctx({ jiraProjects })), tok).toBeNull();
    }
    expect(classifyWorkItemRef("OPS-7", ctx({ jiraProjects: new Set() }))).toBeNull();
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

describe("workItemRefInputs", () => {
  it("does not change when the same ticket comes back from a second query", () => {
    const one = [row("github", "octo/fleet#1")];
    const twice = [...one, { ...row("github", "octo/fleet#1"), queryId: "q2" }];
    expect(workItemRefInputs(null, twice)).toBe(workItemRefInputs(null, one));
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
