// Ticket references written in prose — `#956`, `owner/name#956`, `PROJ-123` — resolved to the
// work item they name (#1659), so the mirror can open the same detail modal a rail row opens.
//
// Pure: every input (the cached rows, the working copy the text is about, the known clones) is
// passed in, so the gates below are testable without a DOM or a store.
import type { WorkItem } from "./read.ts";

/** One reference, already rewritten into the inbox's own key ("owner/name#N" / "PROJ-123"). */
export interface WorkItemRef {
  provider: string;
  key: string;
}

/** Where a GitHub / Bitbucket working copy's origin lives. Only github.com and bitbucket.org
 * qualify: the inbox has no GHE or self-hosted support (docs/log/80 §80.12), so a link there
 * would open a modal that can do nothing for it. */
export interface RefOrigin {
  provider: "github" | "bitbucket";
  /** "owner/name" on that host. */
  path: string;
}

export interface WorkItemRefContext {
  /** The working copy the text is about; null = none, and a bare `#N` then stays text. */
  origin: RefOrigin | null;
  /** The inbox cache (the CP's rows). */
  items: WorkItem[];
  /** "owner/name" → provider for the clones in this workspace, so `owner/name#N` written about
   * a Bitbucket repository is not sent to GitHub. */
  known: Map<string, "github" | "bitbucket">;
}

// A bare or qualified issue number. The look-behind is what keeps `C#`, `&#123;`, `page#12`
// and the middle of a path off it: a citation is preceded by a space, a bracket, a table bar or
// CJK text, never by a word character. `owner/name` is GitHub's own cross-reference syntax.
export const ISSUE_REF_SRC = String.raw`(?<![\w&#/.\-])(?:[A-Za-z0-9][\w.\-]*\/[\w.\-]+)?#[1-9]\d{0,6}(?![\w#])`;
// A Jira key. The shape alone also matches UTF-8, SHA-256, ISO-8601, GPT-4 and P2-1, so this is
// only a candidate: classifyWorkItemRef links it only when its project is one the cache knows.
export const JIRA_REF_SRC = String.raw`(?<![\w/\-])[A-Z][A-Z0-9_]{1,9}-[1-9]\d{0,6}(?![\w\-])`;

/** "Does this text mention anything ticket-shaped at all" — decides whether loading the cache is
 * worth a request. */
export const WORK_ITEM_HINT_RE = new RegExp(`${ISSUE_REF_SRC}|${JIRA_REF_SRC}`);

const ISSUE_TOKEN = /^(?:([\w.-]+\/[\w.-]+))?#(\d+)$/;
const JIRA_TOKEN = /^([A-Z][A-Z0-9_]+)-\d+$/;

/** Whole-token test for the qualified form, so a path-shaped `owner/name#12` in inline code is
 * handed to this linkifier rather than to the file-path one. */
export const isQualifiedIssueToken = (text: string): boolean => /^[A-Za-z0-9][\w.-]*\/[\w.-]+#[1-9]\d{0,6}$/.test(text);

export function originOf(repo: { provider?: string; remote?: string; remotePath?: string } | undefined): RefOrigin | null {
  if (!repo?.remotePath) return null;
  if (repo.provider === "github" && repo.remote === "github.com") return { provider: "github", path: repo.remotePath };
  if (repo.provider === "bitbucket" && repo.remote === "bitbucket.org") return { provider: "bitbucket", path: repo.remotePath };
  return null;
}

/** The project keys the cached Jira rows belong to. */
export function jiraProjects(items: WorkItem[]): Set<string> {
  const out = new Set<string>();
  for (const it of items) {
    if (it.provider !== "jira") continue;
    const m = it.key.match(JIRA_TOKEN);
    if (m) out.add(m[1]);
  }
  return out;
}

const cachedRow = (items: WorkItem[], provider: string, key: string) =>
  items.find((i) => i.provider === provider && i.key === key);

/** Decide whether a matched token is a reference worth a link, and to what. null = leave the
 * text alone.
 *
 * - A bare `#N` needs a context repository on github.com / bitbucket.org. Six and eight digits
 *   are left out: that is a hex colour (`#112233`), and issue numbers that high are rare enough
 *   that the qualified form covers them.
 * - GitHub links optimistically, as a commit hash does. Agents cite pull requests nobody
 *   assigned to the reader, which are exactly the ones missing from the cache, and an unknown
 *   number still opens a panel with a working link to the tracker.
 * - Bitbucket links only cached rows: the inbox knows only its pull requests, and `#N` in a
 *   Bitbucket repository is just as often one of its issues.
 * - Jira links only when the project is one the cache already holds a row of. */
export function classifyWorkItemRef(token: string, ctx: WorkItemRefContext): WorkItemRef | null {
  const issue = token.match(ISSUE_TOKEN);
  if (issue) {
    const [, qualified, num] = issue;
    if (!qualified && (num.length === 6 || num.length === 8)) return null;
    const path = qualified || ctx.origin?.path;
    if (!path) return null;
    const key = `${path}#${num}`;
    for (const p of ["github", "bitbucket"]) {
      if (cachedRow(ctx.items, p, key)) return { provider: p, key };
    }
    const provider = qualified
      ? ctx.known.get(path) || (ctx.origin?.path === path ? ctx.origin.provider : "github")
      : ctx.origin!.provider;
    if (provider === "bitbucket") return null;
    return { provider, key };
  }
  const jira = token.match(JIRA_TOKEN);
  if (jira && jiraProjects(ctx.items).has(jira[1])) return { provider: "jira", key: token };
  return null;
}

/** The row a reference opens: the cached one when there is one, else a reference-only stand-in
 * carrying the key and a constructed tracker URL. The stand-in never claims a title, a state or
 * a kind it does not know; the modal says it is not in the inbox instead. */
export function resolveWorkItemRef(ref: WorkItemRef, items: WorkItem[]): { item: WorkItem; reference: boolean } {
  const hit = cachedRow(items, ref.provider, ref.key);
  if (hit) return { item: hit, reference: false };
  const hash = ref.key.lastIndexOf("#");
  const repo = hash > 0 ? ref.key.slice(0, hash) : "";
  const num = hash >= 0 ? ref.key.slice(hash + 1) : "";
  return {
    reference: true,
    item: {
      id: `ref:${ref.provider}:${ref.key}`,
      queryId: "",
      provider: ref.provider,
      kind: ref.provider === "bitbucket" ? "pr" : "issue",
      key: ref.key,
      title: "",
      state: "",
      url: referenceUrl(ref, repo, num, items),
      assignee: "",
      labels: [],
      labelColors: {},
      repo,
      updatedAt: "",
      checks: { state: "", total: 0, failed: 0, pending: 0 },
      mergeable: "",
    },
  };
}

// GitHub redirects /issues/N to /pull/N when N is a pull request, so one URL serves both. Jira's
// site is not known to the Console except through a cached row's URL, which is also the only way
// a Jira key gets linked at all (classifyWorkItemRef).
function referenceUrl(ref: WorkItemRef, repo: string, num: string, items: WorkItem[]): string {
  if (ref.provider === "github" && repo) return `https://github.com/${repo}/issues/${num}`;
  if (ref.provider === "bitbucket" && repo) return `https://bitbucket.org/${repo}/pull-requests/${num}`;
  if (ref.provider === "jira") {
    for (const it of items) {
      if (it.provider !== "jira") continue;
      const at = it.url.indexOf("/browse/");
      if (at > 0) return `${it.url.slice(0, at)}/browse/${encodeURIComponent(ref.key)}`;
    }
  }
  return "";
}
