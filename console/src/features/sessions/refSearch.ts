// Finding a session by the ticket it is tied to (#1665): the PR whose head is its branch
// (Session.pr) and the issues / tickets it was launched from (the work-item ledger). The left
// pane's filter and the command palette both ask here, so the two agree on what "#1662" finds.
//
// A reference is matched whole, never as a substring or a subsequence: `#12` must not find the
// session whose PR is #1662. That is also why these tokens are kept out of the free-text haystacks.
//
// Pure, so the node tests reach it without a store; the hook over the ledger is useRefIndex.ts.
import type { Session } from "../../types/session.ts";
import type { WorkItemSessionRef } from "../workitems/read.ts";

/** Session slug → the ledger's item keys started in it ("owner/name#45", "PROJ-123"). */
export type RefIndex = Map<string, string[]>;

export function refIndex(ledger: WorkItemSessionRef[] | undefined): RefIndex {
  const idx: RefIndex = new Map();
  for (const r of ledger || []) {
    if (!r.sessionName || !r.itemKey) continue;
    const keys = idx.get(r.sessionName);
    if (!keys) idx.set(r.sessionName, [r.itemKey]);
    else if (!keys.includes(r.itemKey)) keys.push(r.itemKey);
  }
  return idx;
}

export type RefQuery = { num: string; repo: string } | { key: string };

// A leading zero is not a ticket number, and leaving it out keeps `0` and `007` free-text only.
const NUM_Q = /^(?:([\w.-]+\/[\w.-]+))?#?([1-9]\d*)$/;
const JIRA_Q = /^[a-z][a-z0-9_]+-[1-9]\d*$/;
const PR_URL = /^https?:\/\/[^/]+\/([^/]+\/[^/]+)\/pull\/(\d+)/;

/** The query read as a ticket reference, or null for ordinary text. `#45`, `45` and
 * `owner/name#45` are numbers (the qualified form only when the `#` is written); `PROJ-123` is a
 * key. Case-insensitive. */
export function refQuery(q: string): RefQuery | null {
  const s = q.trim().toLowerCase();
  if (!s) return null;
  const m = NUM_Q.exec(s);
  if (m && (!m[1] || s.includes("#"))) return { num: m[2], repo: m[1] || "" };
  if (JIRA_Q.test(s)) return { key: s };
  return null;
}

/** The reference of `s` the query names, as shown to the user ("PR #1662", "owner/name#45",
 * "PROJ-123"), or null. A bare number matches any repository's; a qualified one only its own. */
export function matchSessionRef(s: Session, idx: RefIndex, rq: RefQuery | null): string | null {
  if (!rq) return null;
  if ("key" in rq) return idx.get(s.name)?.find((k) => k.toLowerCase() === rq.key) ?? null;
  if (s.pr && String(s.pr.number) === rq.num) {
    const repo = PR_URL.exec(s.pr.url || "")?.[1]?.toLowerCase() ?? "";
    if (!rq.repo || rq.repo === repo) return "PR #" + s.pr.number;
  }
  for (const k of idx.get(s.name) || []) {
    const i = k.indexOf("#");
    if (i < 0 || k.slice(i + 1) !== rq.num) continue;
    if (!rq.repo || k.slice(0, i).toLowerCase() === rq.repo) return k;
  }
  return null;
}
