// Ticket references written in prose — `#956`, `owner/name#956`, `PROJ-123` — resolved to the
// work item they name (#1659), so the mirror can open the same detail modal a rail row opens.
//
// Pure: every input (the cached rows, the working copy the text is about) is passed in, so the gates below are testable without a DOM or a store.
import type { WorkItem } from "./read.ts";

/** One reference, already rewritten into the inbox's own key ("owner/name#N" / "PROJ-123"). */
export interface WorkItemRef {
  provider: string;
  key: string;
  /** Linked without a cached row (prose only): the host is a guess, re-made when it is clicked. */
  guessed?: boolean;
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
  /** "owner/name" → host for the clones in this workspace, so an uncached `owner/name#N` about a
   * Bitbucket repository is not guessed onto GitHub. */
  known: Map<string, "github" | "bitbucket">;
}

// A bare or qualified issue number. The look-behind is what keeps `C#`, `&#123;`, `page#12`
// and the middle of a path off it: a citation is preceded by a space, a bracket, a table bar or
// CJK text, never by a word character. `owner/name` is GitHub's own cross-reference syntax.
// The number is taken whole, however long and even with a leading zero, so a run of digits is
// never left over for the commit shape to claim (`#11223344`, `#00000000` are colours, not shas);
// classifyWorkItemRef decides what it is.
export const ISSUE_REF_SRC = String.raw`(?<![\w&#/.\-])(?:[A-Za-z0-9][\w.\-]*\/[\w.\-]+)?#\d+(?![\w#])`;
// A Jira key. The shape alone also matches UTF-8, SHA-256, ISO-8601, GPT-4 and P2-1, so this is
// only a candidate: classifyWorkItemRef links it only for a project the cache holds.
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
  // A set: the same ticket matched by two saved queries is one key, not a change.
  const keys = [...new Set(items.map((i) => `${i.provider}:${i.key}`))];
  return [origin ? `${origin.provider}:${origin.path}` : "", keys.sort().join(",")].join("|");
}

const cachedRow = (items: WorkItem[], provider: string, key: string) =>
  items.find((i) => i.provider === provider && i.key === key);

/** Decide whether a matched token is a reference worth a link, and to what. null = leave the
 * text alone.
 *
 * Where the token sits decides how much evidence it needs:
 * - In prose, `#N` is how an agent cites a pull request or an issue, and the ones it cites are
 *   as often merged or closed — gone from the inbox, which holds open items only — as open. So a
 *   GitHub number is linked even when the cache does not hold it (`guessed`, as a commit hash is
 *   linked before it is verified), and a Jira key when the cache holds its project.
 * - In inline code, the same token is usually literal text: a search the user typed (`#166`), a
 *   colour, an example. Almost every number in a busy repository is SOME issue, so existence
 *   proves nothing there; only a ticket the inbox holds is linked.
 *
 * Either way:
 * - A bare `#N` needs a context repository on github.com / bitbucket.org, and is a number on
 *   THAT host: the same owner/name can exist on both, and the other host's row is a different
 *   ticket. Six digits and more than seven are left out: `#112233` / `#11223344` are colours.
 * - `owner/name#N` takes the cached row of either host, the context's host first.
 * - Bitbucket is never guessed: the inbox knows only its pull requests, and `#N` there is as
 *   often one of its issues. */
export function classifyWorkItemRef(token: string, ctx: WorkItemRefContext, inCode = false): WorkItemRef | null {
  const issue = token.match(ISSUE_TOKEN);
  if (issue) {
    const [, qualified, num] = issue;
    if (num.length > 10 || num.startsWith("0")) return null;
    let hosts: string[];
    let key: string;
    if (qualified) {
      key = `${qualified}#${num}`;
      // The text's own host first, whichever repository it names: a Bitbucket session cites
      // Bitbucket tickets.
      hosts = ctx.origin?.provider === "bitbucket" ? ["bitbucket", "github"] : ["github", "bitbucket"];
    } else {
      if (!ctx.origin || num.length === 6 || num.length > 7) return null;
      key = `${ctx.origin.path}#${num}`;
      hosts = [ctx.origin.provider];
    }
    const cached = hosts.find((p) => cachedRow(ctx.items, p, key));
    if (cached) return { provider: cached, key };
    if (inCode) return null;
    const guess = qualified
      ? ctx.known.get(qualified) || (ctx.origin?.path === qualified ? ctx.origin.provider : "github")
      : ctx.origin!.provider;
    return guess === "bitbucket" ? null : { provider: guess, key, guessed: true };
  }
  const jira = token.match(JIRA_TOKEN);
  if (!jira) return null;
  if (cachedRow(ctx.items, "jira", token)) return { provider: "jira", key: token };
  if (!inCode && ctx.items.some((i) => i.provider === "jira" && i.key.startsWith(`${jira[1]}-`))) {
    return { provider: "jira", key: token, guessed: true };
  }
  return null;
}

/** The row a reference opens: the cached one when there is one, else — a guessed reference, or a
 * row that left the cache after the link was drawn — a reference-only stand-in carrying the key and a constructed tracker
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
