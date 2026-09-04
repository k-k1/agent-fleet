// Pure logic that builds a saved Bitbucket query (docs/log/80 §80.23). What is pinned down here
// is that af writes a format nobody can be expected to write by hand, so the assertions look at
// the produced string itself.
import { describe, expect, it } from "vitest";
import { bbNeedsRepo, bbQueries, bbQuery, bbRepoNames, bbWorkspaceOf, bbWorkspaces } from "./bitbucketQuery.ts";

describe("bbQuery", () => {
  it("builds review-requests as repo plus reviewers.uuid=\"@me\", so nobody needs the UUID", () => {
    expect(bbQuery("reviewing", "acme/web")).toBe('acme/web reviewers.uuid="@me"');
  });

  it("builds a repository's PRs from the target alone (Bitbucket defaults to state=OPEN)", () => {
    expect(bbQuery("repo_open", "acme/web")).toBe("acme/web");
  });

  it("scopes my own PRs to the workspace, folding a chosen repository up to it", () => {
    expect(bbQuery("authored", "acme")).toBe("acme");
    expect(bbQuery("authored", "acme/web")).toBe("acme");
  });

  it("refuses a workspace alone for an intent that needs a repository (Bitbucket cannot answer)", () => {
    expect(bbQuery("reviewing", "acme")).toBe("");
    expect(bbQuery("repo_open", "acme")).toBe("");
  });

  it("returns empty when nothing is selected, which blocks saving", () => {
    expect(bbQuery("reviewing", "")).toBe("");
    expect(bbQuery("authored", "   ")).toBe("");
  });

  it("reports the target granularity each intent needs", () => {
    expect(bbNeedsRepo("reviewing")).toBe(true);
    expect(bbNeedsRepo("repo_open")).toBe(true);
    expect(bbNeedsRepo("authored")).toBe(false);
  });
});

describe("bbQueries", () => {
  it("accepts several intents: review-requests plus my own PRs adds two queries at once", () => {
    expect(bbQueries(["reviewing", "authored"], "acme/web")).toEqual(['acme/web reviewers.uuid="@me"', "acme"]);
  });

  it("orders by the on-screen order, not the selection order, so the same picks give the same result", () => {
    expect(bbQueries(["authored", "repo_open", "reviewing"], "acme/web")).toEqual([
      'acme/web reviewers.uuid="@me"',
      "acme/web",
      "acme",
    ]);
  });

  it("drops an intent that cannot be built, e.g. when the target is a workspace only", () => {
    expect(bbQueries(["reviewing", "authored"], "acme")).toEqual(["acme"]);
  });

  it("yields nothing with no intent selected or an empty target, which blocks adding", () => {
    expect(bbQueries([], "acme/web")).toEqual([]);
    expect(bbQueries(["reviewing"], "")).toEqual([]);
  });
});

describe("bbWorkspaces", () => {
  it("de-duplicates and sorts, so a single workspace can be detected and not asked about", () => {
    expect(bbWorkspaces(["acme/web", "acme/api", "zeta/tools"])).toEqual(["acme", "zeta"]);
    expect(bbWorkspaces(["acme/web", "acme/api"])).toEqual(["acme"]);
  });

  it("drops empty and malformed values", () => {
    expect(bbWorkspaces(["", "/web", "acme/web"])).toEqual(["acme"]);
    expect(bbWorkspaceOf("acme/web")).toBe("acme");
  });
});

describe("bbRepoNames", () => {
  it("takes only full_name from the connection listing, sorted", () => {
    const d = { repos: [{ full_name: "zeta/tools" }, { full_name: "acme/web", clone_url: "…" }] };
    expect(bbRepoNames(d)).toEqual(["acme/web", "zeta/tools"]);
  });

  it("collapses a wrong shape, null and error into no candidates, so the caller falls back to free text", () => {
    expect(bbRepoNames(null)).toEqual([]);
    expect(bbRepoNames({ error: { code: "workspace_stopped" } })).toEqual([]);
    expect(bbRepoNames({ repos: null })).toEqual([]);
    expect(bbRepoNames({ repos: [null, { full_name: 42 }, { name: "acme/web" }] })).toEqual([]);
  });

  it("rejects anything not shaped as workspace/repo, which would 404 if passed through", () => {
    expect(bbRepoNames({ repos: [{ full_name: "web" }, { full_name: "acme/web" }] })).toEqual(["acme/web"]);
  });
});
