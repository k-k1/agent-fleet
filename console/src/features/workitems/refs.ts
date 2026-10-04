// Ticket references written in prose — `#956`, `owner/name#956`, `PROJ-123` — resolved to the
// work item they name (#1659), so the mirror can open the same detail modal a rail row opens.
//
// Pure: every input (the cached rows, the working copy the text is about) is passed in, so the gates below are testable without a DOM or a store.
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
  /** The inbox cache (the CP's rows). Only what is here is linked. */
  items: WorkItem[];
}

// A bare or qualified issue number. The look-behind is what keeps `C#`, `&#123;`, `page#12`
// and the middle of a path off it: a citation is preceded by a space, a bracket, a table bar or
// CJK text, never by a word character. `owner/name` is GitHub's own cross-reference syntax.
// The number is taken whole, however long and even with a leading zero, so a run of digits is
// never left over for the commit shape to claim (`#11223344`, `#00000000` are colours, not shas);
// classifyWorkItemRef decides what it is.
export const ISSUE_REF_SRC = String.raw`(?<![\w&#/.\-])(?:[A-Za-z0-9][\w.\-]*\/[\w.\-]+)?#\d+(?![\w#])`;
// A Jira key. The shape alone also matches UTF-8, SHA-256, ISO-8601, GPT-4 and P2-1, so this is
// only a candidate: classifyWorkItemRef links it only when the cache holds that issue.
export const JIRA_REF_SRC = String.raw`(?<![\w/\-])[A-Z][A-Z0-9_]{1,9}-[1-9]\d{0,6}(?![\w\-])`;

/** "Does this text mention anything ticket-shaped at all" — decides whether loading the cache is
 * worth a request. */
export const WORK_ITEM_HINT_RE = new RegExp(`${ISSUE_REF_SRC}|${JIRA_REF_SRC}`);

const ISSUE_TOKEN = /^(?:([\w.-]+\/[\w.-]+))?#(\d+)$/;
const JIRA_TOKEN = /^([A-Z][A-Z0-9_]+)-\d+$/;

/** Whole-token test for the qualified form, so a path-shaped `owner/name#12` in inline code is
 * handed to this linkifier rather than to the file-path one. */
export const isQualifiedIssueToken = (text: string): boolean => /^[A-Za-z0-9][\w.-]*\/[\w.-]+#[1-9]\d*$/.test(text);

export function originOf(repo: { provider?: string; remote?: string; remotePath?: string } | undefined): RefOrigin | null {
  if (!repo?.remotePath) return null;
  if (repo.provider === "github" && repo.remote === "github.com") return { provider: "github", path: repo.remotePath };
  if (repo.provider === "bitbucket" && repo.remote === "bitbucket.org") return { provider: "bitbucket", path: repo.remotePath };
  return null;
}

/** What classifyWorkItemRef's answers depend on beyond the token, as one comparable string: the
 * context origin and the cached keys. A rendered message re-runs its linkifier when this changes —
 * the repository list or the inbox arriving after the text did. */
export function workItemRefInputs(origin: RefOrigin | null, items: WorkItem[]): string {
  const keys = items.map((i) => `${i.provider}:${i.key}`);
  return [origin ? `${origin.provider}:${origin.path}` : "", keys.sort().join(",")].join("|");
}

const cachedRow = (items: WorkItem[], provider: string, key: string) =>
  items.find((i) => i.provider === provider && i.key === key);

/** Decide whether a matched token is a reference worth a link, and to what. null = leave the
 * text alone.
 *
 * Only a ticket the inbox already holds is linked. Prose is full of `#N` that are not citations
 * — a search a user typed (`#166`), a list number, an example — and in a busy repository almost
 * every such number is SOME issue, so "it exists on GitHub" says nothing about whether the
 * writer meant it. Being in the reader's own work item list does.
 *
 * - A bare `#N` needs a context repository on github.com / bitbucket.org, and is a number on
 *   THAT host: the same owner/name can exist on both, and the other host's row is a different
 *   ticket. Six digits and more than seven are left out: `#112233` / `#11223344` are colours.
 * - `owner/name#N` takes the row of either host, the context repository's host first.
 * - A Jira key needs its own row. */
export function classifyWorkItemRef(token: string, ctx: WorkItemRefContext): WorkItemRef | null {
  const issue = token.match(ISSUE_TOKEN);
  if (issue) {
    const [, qualified, num] = issue;
    if (num.length > 10 || num.startsWith("0")) return null;
    let hosts: string[];
    let key: string;
    if (qualified) {
      key = `${qualified}#${num}`;
      hosts = ctx.origin?.path === qualified && ctx.origin.provider === "bitbucket" ? ["bitbucket", "github"] : ["github", "bitbucket"];
    } else {
      if (!ctx.origin || num.length === 6 || num.length > 7) return null;
      key = `${ctx.origin.path}#${num}`;
      hosts = [ctx.origin.provider];
    }
    const provider = hosts.find((p) => cachedRow(ctx.items, p, key));
    return provider ? { provider, key } : null;
  }
  if (JIRA_TOKEN.test(token) && cachedRow(ctx.items, "jira", token)) return { provider: "jira", key: token };
  return null;
}

/** The row a reference opens: the cached one when there is one, else — the row left the cache
 * after the link was drawn — a reference-only stand-in carrying the key and a constructed tracker
 * URL. The stand-in never claims a title, a state or
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
// site is not known to the Console except through a cached row's URL.
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
