// Pure logic of the SVN Show log / local changes panes (issue #1705). The Agent already
// parses svn's XML (workspace/agent/svn_view.go); what lives here is what the panes do with
// the parsed answer: paging, marking revisions against the working copy, status wording.
// No React, no fetch — so the rules are unit-tested without mounting anything.

export interface SvnLogPath {
  action: string; // A / M / D / R
  path: string; // repository-root relative, e.g. /trunk/a.txt
  kind?: string;
  copyFromPath?: string;
  copyFromRev?: string;
}

export interface SvnRevision {
  rev: number;
  author: string;
  date: string;
  message: string;
  paths: SvnLogPath[];
}

export interface SvnLogPage {
  revisions: SvnRevision[];
  hasMore: boolean;
  /** The working copy's own revision, as svn prints it ("" when unknown). */
  wcRevision: string;
}

export interface SvnChange {
  path: string;
  status: string; // svn's one-letter code: M A D R C ? ! ~
  props?: string;
  untracked: boolean;
  conflict: boolean;
}

/** `api/repos/<repo>/svn-log?...` — `from` is the OLDEST revision already shown. */
export function svnLogUrl(repo: string, opts: { path?: string; from?: number; limit?: number } = {}): string {
  const q: string[] = [];
  if (opts.limit) q.push(`limit=${opts.limit}`);
  const path = normalizeFilterPath(opts.path ?? "");
  if (path) q.push(`path=${encodeURIComponent(path)}`);
  if (opts.from !== undefined && opts.from > 0) q.push(`from=${opts.from}`);
  return `api/repos/${encodeURIComponent(repo)}/svn-log${q.length ? "?" + q.join("&") : ""}`;
}

/** What a user types into the path filter, reduced to the working-copy-relative form the
 * Agent accepts: no leading "/" or "./", no trailing "/". The Agent still validates. */
export function normalizeFilterPath(input: string): string {
  let p = input.trim().replace(/\\/g, "/");
  p = p.replace(/^(\.\/)+/, "").replace(/^\/+/, "").replace(/\/+$/, "");
  return p === "." ? "" : p;
}

/** The oldest revision in a (newest-first) list — the `from` of the next page. */
export function oldestRev(revs: SvnRevision[]): number | undefined {
  let min: number | undefined;
  for (const r of revs) if (min === undefined || r.rev < min) min = r.rev;
  return min;
}

/** Append a page of older revisions, newest first, dropping any revision already shown (a
 * commit landing between two page loads shifts nothing here because paging is by revision
 * number, not offset — but a double click on "load more" must not duplicate rows). */
export function mergeLogPage(shown: SvnRevision[], page: SvnRevision[]): SvnRevision[] {
  const seen = new Set(shown.map((r) => r.rev));
  const add = page.filter((r) => !seen.has(r.rev));
  return [...shown, ...add].sort((a, b) => b.rev - a.rev);
}

export type RevSide = "newer" | "current" | "older" | "unknown";

/** Which side of the working copy's revision an entry is on. "newer" means the working copy
 * has not been updated to it yet. Entries are compared against the revision the working copy
 * was LAST UPDATED to — the same number `svn info` calls Revision. */
export function revSide(rev: number, wcRevision: string | undefined): RevSide {
  const wc = Number.parseInt(wcRevision ?? "", 10);
  if (!Number.isFinite(wc) || wc <= 0) return "unknown";
  if (rev > wc) return "newer";
  return rev === wc ? "current" : "older";
}

/** The first line of a commit message (the list row), "" for an empty message. */
export function messageSubject(msg: string): string {
  return (msg.split("\n", 1)[0] ?? "").trim();
}

/** A revision's date as YYYY-MM-DD; the Agent passes svn's ISO-8601 string through. */
export function revDay(date: string): string {
  return (date || "").slice(0, 10);
}

/** Whether an api() result is the Agent's "credential missing or refused" answer — the cue to
 * open the re-authentication dialog instead of printing an error. Keyed off the machine code
 * only; svn's wording is never matched in the browser. */
export function isSvnAuthError(d: unknown): boolean {
  const e = (d as { error?: { code?: string } } | null | undefined)?.error;
  return !!e && typeof e === "object" && e.code === "svn_auth_required";
}

export type ChangeTone = "staged" | "unstaged" | "untracked" | "conflict";

/** The colour class and the i18n key suffix of an `svn status` letter. */
export function changeTone(c: Pick<SvnChange, "status" | "untracked" | "conflict">): ChangeTone {
  if (c.conflict) return "conflict";
  if (c.untracked) return "untracked";
  return c.status === "A" || c.status === "R" ? "staged" : "unstaged";
}

type StatusKey =
  | "svn.status_modified"
  | "svn.status_added"
  | "svn.status_deleted"
  | "svn.status_replaced"
  | "svn.status_conflicted"
  | "svn.status_unversioned"
  | "svn.status_missing"
  | "svn.status_obstructed";

const STATUS_KEY: Record<string, StatusKey> = {
  M: "svn.status_modified",
  A: "svn.status_added",
  D: "svn.status_deleted",
  R: "svn.status_replaced",
  C: "svn.status_conflicted",
  "?": "svn.status_unversioned",
  "!": "svn.status_missing",
  "~": "svn.status_obstructed",
};

/** i18n key for a status letter; unknown letters read as "modified". */
export function statusKey(letter: string): StatusKey {
  return STATUS_KEY[letter] ?? "svn.status_modified";
}

/** Order for the changes list: conflicts first (they block work), then the rest by path. */
export function sortChanges(cs: SvnChange[]): SvnChange[] {
  return [...cs].sort((a, b) => Number(b.conflict) - Number(a.conflict) || a.path.localeCompare(b.path));
}

/** Split a Files-tree row path ("repos/<name>/sub/x.txt") into the working copy and the path
 * inside it. null for anything outside ~/repos. */
export function splitRepoPath(rowPath: string): { repo: string; sub: string } | null {
  if (!rowPath.startsWith("repos/")) return null;
  const rest = rowPath.slice("repos/".length);
  const i = rest.indexOf("/");
  const repo = i < 0 ? rest : rest.slice(0, i);
  if (!repo) return null;
  return { repo, sub: i < 0 ? "" : rest.slice(i + 1) };
}

/** Summarise a revision's changed paths as "3 M · 1 A" for the list row. */
export function actionSummary(paths: SvnLogPath[]): string {
  const n = new Map<string, number>();
  for (const p of paths) n.set(p.action, (n.get(p.action) ?? 0) + 1);
  return ["A", "M", "D", "R"]
    .filter((a) => n.has(a))
    .map((a) => `${n.get(a)} ${a}`)
    .join(" · ");
}
