// sessionLinks — what a session row's link line says about its branch's pull request (#1062).
// Pure, so the wording and the tones are pinned by node tests; SessionLinks.tsx draws it.
import { browserTarget } from "../browser/target.ts";
import type { Session, SessionPR, SessionWorkLink, SessionWorkLinkRole } from "../../types/session.ts";

export type PRStateKey = "open" | "draft" | "merged" | "closed";

/** The PR's state as the row names it: a draft is its own state, as on GitHub. */
export function prStateKey(pr: SessionPR): PRStateKey {
  if (pr.state === "merged") return "merged";
  if (pr.state === "closed") return "closed";
  return pr.draft ? "draft" : "open";
}

export const PR_ICON: Record<PRStateKey, string> = {
  open: "git-pull-request",
  draft: "git-pull-request-draft",
  merged: "git-merge",
  closed: "git-pull-request-closed",
};

/** The CI mark, shown for an open PR only: a red run on a PR that is already merged or
 * closed is nothing to act on. Absent checks draw nothing — never green. */
export function prChecks(pr: SessionPR): "success" | "failure" | "pending" | null {
  if (pr.state !== "open" || !pr.checks) return null;
  return pr.checks;
}

export const CHECK_ICON: Record<"success" | "failure" | "pending", string> = {
  success: "pass",
  failure: "error",
  pending: "clock",
};

/** A GitHub pull request page, and nothing else, may become the row's href: the URL arrives
 * from the provider through two relays. Only https://github.com/<owner>/<repo>/pull/<n> with no
 * port and no userinfo passes, and it is rebuilt from those parts, so a query or a fragment
 * never rides along. */
const PR_PATH = /^\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/pull\/[1-9][0-9]*$/;

export function safePRURL(url: string): string | null {
  try {
    const u = new URL(url);
    if (u.protocol !== "https:" || u.hostname !== "github.com" || u.port !== "" || u.username || u.password) return null;
    return PR_PATH.test(u.pathname) ? `https://github.com${u.pathname}` : null;
  } catch {
    return null;
  }
}

/** Ports the row offers, in order. A port the browser pane would refuse (the Agent's own,
 * or out of range) is dropped rather than drawn as a link that cannot open. */
export function rowPorts(ports: number[] | undefined): number[] {
  if (!ports) return [];
  return ports.filter((p) => browserTarget(p, "/") !== null);
}

/** The work items a row names (#3134), strongest role first. The branch's own PR is left out
 * while the row draws it as the PR chip: the same PR twice on one line says nothing new. Keys
 * compare case-insensitively, as the Agent compares them (owner/repo names are). */
export function rowWorkLinks(s: Pick<Session, "links" | "pr">): SessionWorkLink[] {
  const prKey = s.pr ? prKeyOf(s.pr.url) : null;
  return (s.links || [])
    .filter((l) => l.key && !(prKey && sameWorkItem(l, "github", prKey)))
    .map((l, i) => ({ l, i }))
    .sort((a, b) => roleRank(b.l.role) - roleRank(a.l.role) || a.i - b.i)
    .map(({ l }) => l);
}

/** Whether a link names provider/key, ignoring case on both, as the Agent does. */
export function sameWorkItem(l: { provider: string; key: string }, provider: string, key: string): boolean {
  return l.provider.toLowerCase() === provider.toLowerCase() && l.key.toLowerCase() === key.toLowerCase();
}

const ROLE_RANK: Record<SessionWorkLinkRole, number> = { started: 5, created: 4, working: 3, reviewing: 2, watching: 1 };

/** A role the Console knows, or null: the value arrives from the Agent's meta, and an older or
 * hand-edited one must not become a class name or an i18n key. */
export function knownRole(role: string): SessionWorkLinkRole | null {
  return Object.hasOwn(ROLE_RANK, role) ? (role as SessionWorkLinkRole) : null;
}

const roleRank = (role: string): number => {
  const known = knownRole(role);
  return known ? ROLE_RANK[known] : 0;
};

export const LINK_ROLE_ICON: Record<SessionWorkLinkRole, string> = {
  started: "rocket",
  created: "git-pull-request-create",
  working: "tools",
  reviewing: "eye",
  watching: "bell",
};

/** "owner/repo#N" of a github.com PR page, as the Agent keys it. */
function prKeyOf(url: string): string | null {
  const safe = safePRURL(url);
  if (!safe) return null;
  const [, owner, repo, , n] = new URL(safe).pathname.split("/");
  return `${owner}/${repo}#${n}`;
}
