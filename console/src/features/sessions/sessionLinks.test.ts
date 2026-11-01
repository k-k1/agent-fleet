import { describe, expect, it } from "vitest";
import { knownRole, linkDone, linkPRState, prChecks, prStateKey, rowPorts, rowWorkLinks, safePRURL } from "./sessionLinks.ts";

describe("sessionLinks", () => {
  it("names a draft apart from an open PR", () => {
    expect(prStateKey({ number: 1, state: "open", url: "" })).toBe("open");
    expect(prStateKey({ number: 1, state: "open", draft: true, url: "" })).toBe("draft");
    expect(prStateKey({ number: 1, state: "merged", draft: true, url: "" })).toBe("merged");
    expect(prStateKey({ number: 1, state: "closed", url: "" })).toBe("closed");
  });

  it("shows CI on open PRs only, and never invents green", () => {
    expect(prChecks({ number: 1, state: "open", url: "", checks: "pending" })).toBe("pending");
    expect(prChecks({ number: 1, state: "open", url: "" })).toBeNull();
    expect(prChecks({ number: 1, state: "closed", url: "", checks: "failure" })).toBeNull();
  });

  it("accepts bitbucket.org pull request pages and rebuilds them", () => {
    expect(safePRURL("https://bitbucket.org/w/r/pull-requests/3")).toBe("https://bitbucket.org/w/r/pull-requests/3");
    expect(safePRURL("https://bitbucket.org/w/r/pull-requests/3/diff?x=1#y")).toBeNull();
    expect(safePRURL("https://bitbucket.org:443/w/r/pull-requests/3?x=1")).toBe("https://bitbucket.org/w/r/pull-requests/3");
    // Each host's own path only: a GitHub path on Bitbucket is not a PR page.
    expect(safePRURL("https://bitbucket.org/w/r/pull/3")).toBeNull();
    expect(safePRURL("https://github.com/o/r/pull-requests/3")).toBeNull();
    expect(safePRURL("https://bitbucket.org.evil.example/w/r/pull-requests/3")).toBeNull();
    expect(safePRURL("https://u:p@bitbucket.org/w/r/pull-requests/3")).toBeNull();
    // A host name that is an Object.prototype key is not a provider.
    expect(safePRURL("https://constructor/w/r/pull-requests/3")).toBeNull();
  });

  it("does not repeat a Bitbucket row PR as a link, but keeps a GitHub item of the same key", () => {
    const links = rowWorkLinks({
      pr: { number: 3, state: "open", url: "https://bitbucket.org/w/r/pull-requests/3" },
      links: [
        { provider: "bitbucket", key: "W/R#3", role: "working" },
        { provider: "github", key: "w/r#3", role: "watching" },
      ],
    });
    expect(links.map((l) => `${l.provider}:${l.key}`)).toEqual(["github:w/r#3"]);
  });

  it("accepts github.com pull request pages only", () => {
    expect(safePRURL("https://github.com/o/r/pull/1")).toBe("https://github.com/o/r/pull/1");
    expect(safePRURL("http://github.com/o/r/pull/1")).toBeNull();
    expect(safePRURL("https://github.com.evil.example/o/r/pull/1")).toBeNull();
    expect(safePRURL("javascript:alert(1)")).toBeNull();
    expect(safePRURL("")).toBeNull();
    // github.com, but not a pull request page / not the plain origin.
    expect(safePRURL("https://github.com/login/oauth/authorize?client_id=x")).toBeNull();
    expect(safePRURL("https://github.com/o/r/issues/2")).toBeNull();
    expect(safePRURL("https://github.com/o/r/pull/0")).toBeNull();
    expect(safePRURL("https://github.com/o/r/pull/1/files")).toBeNull();
    expect(safePRURL("https://u:p@github.com/o/r/pull/2")).toBeNull();
    expect(safePRURL("https://github.com:444/o/r/pull/2")).toBeNull();
    // The default port spelled out is the same origin; query and fragment are dropped.
    expect(safePRURL("https://github.com:443/o/r/pull/2?x=1#y")).toBe("https://github.com/o/r/pull/2");
  });

  it("drops ports the browser pane would refuse", () => {
    expect(rowPorts([3000, 7700, 0, 70000, 8080])).toEqual([3000, 8080]);
    expect(rowPorts(undefined)).toEqual([]);
  });

  it("orders linked items by role and drops the PR the row already draws", () => {
    const links = rowWorkLinks({
      pr: { number: 7, state: "open", url: "https://github.com/o/r/pull/7" },
      links: [
        { provider: "jira", key: "PROJ-2", role: "watching" },
        { provider: "github", key: "o/r#7", role: "working" },
        { provider: "github", key: "o/r#3", role: "working" },
        { provider: "jira", key: "PROJ-1", role: "started" },
      ],
    });
    expect(links.map((l) => l.key)).toEqual(["PROJ-1", "o/r#3", "PROJ-2"]);
    expect(rowWorkLinks({})).toEqual([]);
  });

  it("puts a merged or closed PR after every item still in flight, whatever its role (#3166)", () => {
    const links = rowWorkLinks({
      links: [
        { provider: "github", key: "o/r#1", role: "created", state: "merged" },
        { provider: "jira", key: "PROJ-2", role: "watching" },
        { provider: "github", key: "o/r#3", role: "working", state: "closed" },
        { provider: "github", key: "o/r#4", role: "working", state: "open" },
      ],
    });
    expect(links.map((l) => l.key)).toEqual(["o/r#4", "PROJ-2", "o/r#1", "o/r#3"]);
    expect([linkDone({ state: "merged" }), linkDone({ state: "closed" }), linkDone({ state: "draft" }), linkDone({})]).toEqual([
      true,
      true,
      false,
      false,
    ]);
    expect([linkPRState({ state: "draft" }), linkPRState({ state: "odd" as never }), linkPRState({})]).toEqual(["draft", null, null]);
  });

  it("compares keys case-insensitively and sorts an unknown role last", () => {
    const links = rowWorkLinks({
      pr: { number: 7, state: "open", url: "https://github.com/O/R/pull/7" },
      links: [
        { provider: "jira", key: "PROJ-3", role: "bogus" as never },
        { provider: "GitHub", key: "o/r#7", role: "working" },
        { provider: "jira", key: "PROJ-2", role: "watching" },
      ],
    });
    expect(links.map((l) => l.key)).toEqual(["PROJ-2", "PROJ-3"]);
    expect(knownRole("watching")).toBe("watching");
    expect(knownRole("bogus")).toBeNull();
    expect(knownRole("constructor")).toBeNull();
  });
});
