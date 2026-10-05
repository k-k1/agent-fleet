// SvnLogView — "Show log" for an SVN working copy (issue #1705). A linear revision list (SVN
// history is linear per path), a path filter, "load more", and a mark on revisions newer than
// the working copy. The log is a NETWORK call on the server: it is fetched when the pane opens,
// when the filter is applied, and when the user asks for more — never from a timer.
import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, errText, isTransientErr } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { SvnAuthModal } from "../repos/SvnAuthModal.tsx";
import { openChanges, openCommit } from "./open.ts";
import { actionSummary, isSvnAuthError, mergeLogPage, messageSubject, normalizeFilterPath, oldestRev, revDay, revSide, svnLogUrl } from "./svnLog.ts";
import type { SvnLogPage, SvnRevision } from "./svnLog.ts";

const PAGE = 50;

export function SvnLogView({ repo, path = "", headerActions }: { repo: string; path?: string; headerActions?: ReactNode }) {
  const tr = useT();
  const [filter, setFilter] = useState(path);
  const [applied, setApplied] = useState(normalizeFilterPath(path));
  const [revs, setRevs] = useState<SvnRevision[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [wcRev, setWcRev] = useState("");
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const [authOpen, setAuthOpen] = useState(false);
  const [selected, setSelected] = useState(0);
  // A response for a request that was superseded (another filter, a repo switch) must not
  // land: each request takes a ticket and only the latest one commits.
  const ticket = useRef(0);

  // `more` = append the page older than what is shown; otherwise start over for `applied`.
  const load = useCallback(
    async (more: boolean, current: SvnRevision[], pathFilter: string) => {
      const mine = ++ticket.current;
      setLoading(true);
      setErr("");
      let d;
      try {
        d = await api(svnLogUrl(repo, { limit: PAGE, path: pathFilter, from: more ? oldestRev(current) : undefined }));
      } catch {
        if (mine === ticket.current) {
          setErr(tr("svn.log_failed", { err: tr("err.unknown") }));
          setLoading(false);
        }
        return;
      }
      if (mine !== ticket.current) return;
      setLoading(false);
      if (isSvnAuthError(d)) {
        setAuthOpen(true);
        return;
      }
      if (d?.error) {
        setErr(tr("svn.log_failed", { err: errText(d.error) }));
        return;
      }
      if (isTransientErr(d)) return;
      const page = d as SvnLogPage;
      setRevs((prev) => (more ? mergeLogPage(prev, page.revisions || []) : mergeLogPage([], page.revisions || [])));
      setHasMore(!!page.hasMore);
      setWcRev(page.wcRevision || "");
    },
    [repo, tr],
  );

  // Open / repo switch → first page. Deliberately no interval and no focus trigger.
  useEffect(() => {
    setRevs([]);
    setHasMore(false);
    void load(false, [], applied);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repo, applied]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const next = normalizeFilterPath(filter);
    if (next === applied) void load(false, [], next); // same filter = refetch
    else setApplied(next);
  };

  const select = (r: SvnRevision) => {
    setSelected(r.rev);
    openCommit(repo, String(r.rev), applied || undefined);
  };

  return (
    <div className="scmview">
      <ViewHead
        actions={
          <>
            <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.changes_title")} onClick={() => openChanges(repo)}>
              <Icon name="git-commit" />
            </button>
            <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.refresh")} onClick={() => void load(false, [], applied)}>
              <Icon name="refresh" />
            </button>
            {headerActions}
          </>
        }
      >
        <span className="view-title" title={repo}>
          <Icon name="history" /> {repo} — {tr("svn.log")}
        </span>
        {wcRev && <span className="scm-repo-tag">r{wcRev}</span>}
      </ViewHead>
      <form className="svnlog-filter" onSubmit={submit}>
        <input
          type="text"
          value={filter}
          placeholder={tr("svn.log_path_filter")}
          aria-label={tr("svn.log_path_filter")}
          onChange={(e) => setFilter(e.target.value)}
        />
        <button type="submit" className="ui-btn ui-btn-sm" disabled={loading}>
          <Icon name="filter" /> {tr("svn.log_filter_apply")}
        </button>
        {applied && (
          <button
            type="button"
            className="ui-btn ui-btn-ghost ui-btn-sm"
            onClick={() => {
              setFilter("");
              setApplied("");
            }}
          >
            {tr("svn.log_filter_clear")}
          </button>
        )}
      </form>
      <div className="scm-scroll">
        {err && <div className="svnlog-err">{err}</div>}
        {!err && !loading && revs.length === 0 && <EmptyState icon="history" title={tr("svn.log_empty")} />}
        <ul className="svnlog">
          {revs.map((r) => {
            const side = revSide(r.rev, wcRev);
            return (
              <li key={r.rev} className={"svnlog-row" + (selected === r.rev ? " active" : "") + (side === "newer" ? " newer" : "")}>
                <button type="button" className="svnlog-btn" onClick={() => select(r)}>
                  <code className="svnlog-rev">r{r.rev}</code>
                  <span className="svnlog-msg" title={r.message}>
                    {messageSubject(r.message) || tr("scm.no_message")}
                  </span>
                  {side === "newer" && (
                    <span className="svnlog-tag newer" title={tr("svn.log_newer_hint", { rev: wcRev })}>
                      {tr("svn.log_newer")}
                    </span>
                  )}
                  {side === "current" && <span className="svnlog-tag current">{tr("svn.log_current")}</span>}
                  <span className="svnlog-meta">
                    {r.author || tr("svn.log_no_author")} · {revDay(r.date)}
                    {r.paths.length > 0 ? ` · ${actionSummary(r.paths)}` : ""}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
        {loading && <div className="svnlog-loading">{tr("scm.loading")}</div>}
        {hasMore && !loading && (
          <button type="button" className="ui-btn ui-btn-ghost svnlog-more" onClick={() => void load(true, revs, applied)}>
            {tr("svn.log_load_more")}
          </button>
        )}
      </div>
      {authOpen && (
        <SvnAuthModal
          repo={repo}
          onClose={() => setAuthOpen(false)}
          onSaved={() => {
            setAuthOpen(false);
            void load(false, [], applied);
          }}
        />
      )}
    </div>
  );
}
