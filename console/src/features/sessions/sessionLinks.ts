// sessionLinks — what a session row's link line says about its branch's pull request (#1062).
// Pure, so the wording and the tones are pinned by node tests; SessionLinks.tsx draws it.
import { browserTarget } from "../browser/target.ts";
import type { SessionPR } from "../../types/session.ts";

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
 * from the provider through two relays, and a javascript: or foreign link must not. */
export function safePRURL(url: string): string | null {
  try {
    const u = new URL(url);
    return u.protocol === "https:" && u.hostname === "github.com" ? u.href : null;
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
