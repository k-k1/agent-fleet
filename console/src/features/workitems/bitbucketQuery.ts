// Builds a saved Bitbucket query from an intent and a target (docs/log/80 §80.22).
//
// This is the one provider that departs from "the query field is itself the filter UI"
// (§80.16-1), because only the Bitbucket query contains inventions of af's own:
//   1. The leading `<workspace>[/<repo>]` is not Bitbucket syntax; it is af's convention for
//      telling an API with no cross-repository search where to look (§80.19.1).
//   2. The review-request expression needs `reviewers.uuid="{b8ceb65c-...}"`. An expansion of
//      `@me` was added, but nobody reaches `@me` without already knowing that shape.
// GitHub search syntax and JQL are real dialects users write every day, so those pass through.
//
// Measured: the default `workspace/repo reviewers.uuid="@me"` was saved verbatim and came back as
// "bitbucket has no workspace/repo visible to this connection". Putting the words that must be
// replaced into the default, so that the error explains itself, did not work in practice.

export type BbIntent = "reviewing" | "repo_open" | "authored";

/** These three and no more: the Bitbucket API offers nothing else (§80.19.1). */
export const BB_INTENTS: BbIntent[] = ["reviewing", "repo_open", "authored"];

/** Whether the intent needs a repository as well. Only `authored` can be queried per workspace
 *  (`/2.0/workspaces/{ws}/pullrequests/{user}` is the only route that returns the PRs a given
 *  person opened). */
export function bbNeedsRepo(intent: BbIntent): boolean {
  return intent !== "authored";
}

export function bbWorkspaceOf(fullName: string): string {
  return (fullName.split("/")[0] || "").trim();
}

/** The de-duplicated workspace list behind a set of `workspace/repo` names. */
export function bbWorkspaces(fullNames: string[]): string[] {
  const seen = new Set<string>();
  for (const fn of fullNames) {
    const w = bbWorkspaceOf(fn);
    if (w) seen.add(w);
  }
  return [...seen].sort();
}

/** Intent plus target -> the query to save. Returns "" when it cannot be built, which blocks
 *  saving. */
export function bbQuery(intent: BbIntent, target: string): string {
  const t = target.trim();
  if (!t) return "";
  if (intent === "authored") return bbWorkspaceOf(t);
  // An intent that needs a repository must not be given a workspace alone; Bitbucket cannot
  // answer that.
  if (!t.includes("/")) return "";
  return intent === "reviewing" ? `${t} reviewers.uuid="@me"` : t;
}

/** One or more intents plus a target -> the queries to save.
 *
 * Several can be added at once because the three intents are not exclusive: wanting both "waiting
 * on my review" and "my own PRs" is normal, and adding them one at a time would make the user
 * pick the same target twice. The output follows BB_INTENTS order and drops duplicates, so no
 * combination that happens to render to the same string can ever produce two identical rows. */
export function bbQueries(intents: BbIntent[], target: string): string[] {
  const out: string[] = [];
  for (const intent of BB_INTENTS) {
    if (!intents.includes(intent)) continue;
    const q = bbQuery(intent, target);
    if (q && !out.includes(q)) out.push(q);
  }
  return out;
}

/** Boundary that extracts just the full_name values from a `/connections/git/bitbucket.org/repos`
 * response.
 *
 * A wrong shape, an `error` field and a null all collapse to "no candidates". The caller falls
 * back to free text when there are none, so throwing here buys nothing (§80.17.5: fixing only the
 * producer still leaves older peers sending the old shape). */
export function bbRepoNames(d: unknown): string[] {
  const list = (d as { repos?: unknown } | null)?.repos;
  if (!Array.isArray(list)) return [];
  const out: string[] = [];
  for (const r of list) {
    const fn = (r as { full_name?: unknown } | null)?.full_name;
    if (typeof fn === "string" && fn.includes("/")) out.push(fn.trim());
  }
  return out.sort();
}
