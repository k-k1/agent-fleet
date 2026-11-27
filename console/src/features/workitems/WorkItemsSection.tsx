// WorkItemsSection (docs/log/80 P0) — the left-rail inbox of external tickets (GitHub Issue
// and PR; Jira in P1). Membership-scoped and Control-Plane persisted like the memo queue,
// so it renders in BOTH the running and the stopped rail — picking a ticket happens
// before a session exists, which is exactly when the Workspace tends to be stopped.
//
// This is not a ticket viewer (docs/log/80 §80.1). No query builder, no detail pane, no
// sort control: composing what to fetch is the saved query's job, and the row's job is to
// start a session with the ticket's context already in place. Anything more is a worse
// copy of the tracker's own web UI.
//
// The line moved once, on real data (docs/log/80 §80.18 / ADR 0061 decision 14). One saved
// query returned 41 rows, so past RAIL_VISIBLE the rail offers a one-line filter over the
// rows it already has. Every row stays rendered and the rail scrolls; a show-more fold only
// put a press in front of each row past the tenth. The filter does not touch the provider,
// is not saved, and does not reorder —
// that is the whole distinction between "the rail's job" and "the query's job". The one
// exception is a press, not a keystroke (#1095): when the filter finds nothing, or a query's
// page left matches out, "search the tracker" asks the provider once and shows what it found
// under the rail's rows, uncached.
//
// No buttons on the row (§80.20). Forty-one "start" buttons down the right edge make the rail
// look like a surface where pressing does something, which is alarming for a list meant to be
// read. The row is information again and every control lives in the detail modal the row opens.
// Only two things stay on the row: the external link (going straight to the tracker) and the
// started badge (the information that stops a second person picking up the same ticket).
//
// Launching from the detail modal still just hands the existing launch stack (seed ->
// useLaunchTarget -> LaunchModal), so worktree/branch/agent stay implemented in one place.
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useWorkItemStore, startWorkItemPolling } from "./store.ts";
import { useWorkItemModal } from "./modal.ts";
import { useOpenWorkItemSession } from "./WorkItemModalHost.tsx";
import { WorkItemQueryModal } from "./WorkItemQueryModal.tsx";
import { JiraWorkTypeIcon } from "./JiraWorkTypeIcon.tsx";
import { WorkItemStateIcon } from "./WorkItemStateIcon.tsx";
import { LabelBadge } from "./LabelBadge.tsx";
import { resolveSessionRef } from "./sessionRefs.ts";
import {
  checksText,
  checksTone,
  dedupeWorkItems,
  fullLocal,
  matchWorkItem,
  RAIL_VISIBLE,
  railLabels,
  railWhen,
  sessionsForItem,
  shortKey,
  shortLocal,
  sortWorkItems,
  stateLabel,
  stateTone,
  workItemIcon,
  uniformMeta,
  readWorkItemSearch,
  type WorkItem,
  type WorkItemSearchResult,
  type WorkItemSessionRef,
} from "./read.ts";
import { workItemSearch } from "./api.ts";
import { errText } from "../../core/api/client.ts";
import { unreadOf, useSeenStore, type Unread } from "./seen.ts";
import "./workitems.css";

interface RowProps {
  item: WorkItem;
  started: WorkItemSessionRef[];
  /** What the started badge calls the first session: its display name when it is on the live
   * list, else its slug (#1108). */
  startedName: string;
  /** Meta this query repeats on every row — dropped from the line (docs/log/80 §80.18.2). */
  uniform: { repo: boolean; assignee: boolean };
  /** What changed since the user last looked (#2779); "" = nothing. */
  unread: Unread;
  onOpen(item: WorkItem): void;
  onOpenSession(name: string): void;
}

const CHECK_ICON: Record<string, string> = { success: "pass", failure: "error", pending: "clock" };

const WorkItemRow = memo(function WorkItemRow({ item, started, startedName, uniform, unread, onOpen, onOpenSession }: RowProps) {
  const tr = useT();
  const tone = stateTone(item);
  const stateText = stateLabel(item.state);
  const jira = item.provider === "jira";
  const busy = started.length > 0;
  // The row shows only what differs between rows (docs/log/80 §80.18.2). An assignee or repo
  // that is the same on every row is dropped, and if nothing is left the second line is not
  // drawn at all — the default Jira query collapses to one line here, halving the rail's height.
  // The freed height is not filled back in.
  const repo = uniform.repo ? "" : item.repo;
  const assignee = uniform.assignee ? "" : item.assignee;
  const labels = railLabels(item.labels);
  const meta = !!(repo || assignee || labels.length);
  const when = railWhen(item.updatedAt);
  // The row is one button, so its label is all a screen reader announces — the icons inside it
  // are not read out. The CI and conflict status therefore go into the label as well.
  const ciText = item.checks.state ? `${tr("wi.detail_checks")}: ${checksText(item.checks)}` : "";
  const conflictText = item.mergeable === "conflict" ? tr("wi.detail_merge_conflict") : "";
  const unreadText = unread ? tr(unread === "comments" ? "wi.unread_comments" : "wi.unread_updated") : "";
  const commentCount = item.commentCount ?? 0;
  const issueState = jira || (item.kind === "issue" && item.provider === "github") ? stateText : "";
  const label = [
    tr("wi.open_detail", { key: item.key }), jira ? item.type : "", issueState, unreadText, ciText, conflictText,
  ].filter(Boolean).join(" — ");
  return (
    // The whole row opens the detail modal. The external link and the started badge nested
    // inside it are controls of their own, so each stops propagation before acting; otherwise
    // one press would open two things.
    <div
      className={"wi-row" + (item.state === "done" ? " wi-row--done" : "") + (jira ? " wi-row--jira" : "") + (unread ? " is-unread" : "")}
      role="button"
      tabIndex={0}
      aria-label={label}
      onClick={() => onOpen(item)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen(item);
        }
      }}
    >
      {jira ? <JiraWorkTypeIcon type={item.type} /> : (
        <span className={`wi-dot tone-${tone}`} title={stateText}>
          <WorkItemStateIcon name={workItemIcon(item)} />
        </span>
      )}
      <div className="wi-info">
        <div className="wi-head">
          {unread && <span className="wi-unread" role="img" aria-label={unreadText} title={unreadText} />}
          <span className="wi-key" title={item.key}>
            {shortKey(item.key)}
          </span>
          {jira && <span className={`wi-jira-state wi-jira-state--${item.state}`} title={stateText} aria-hidden="true" />}
          <span className="wi-title" title={item.assignee ? `${item.title} — @${item.assignee}` : item.title}>
            {item.title}
          </span>
          {/* CI and conflicts of an open pull request (#1113), so the rail answers "which of these
              needs me" without opening each one. Icons only, with the detail modal's own wording
              on hover: the title keeps the width. Nothing is drawn when nothing was read — an
              issue, a closed PR, a provider without these — and "no checks" is that same nothing,
              never a green mark. The same text is in the row's label above. */}
          {ciText && (
            <span className={`wi-flag tone-${checksTone(item.checks)}`} title={ciText}>
              <Icon name={CHECK_ICON[item.checks.state] || "circle-large-outline"} />
            </span>
          )}
          {conflictText && (
            <span className="wi-flag tone-bad" title={conflictText}>
              <Icon name="git-merge" />
            </span>
          )}
          {/* The count comes with the list response (GitHub, Bitbucket), so it costs no request per
              row; Jira's list does not carry one and its rows simply have none. */}
          {commentCount > 0 && (
            <span className="wi-comments" title={tr("wi.comment_count", { n: commentCount })}>
              <Icon name="comment" />
              {commentCount}
            </span>
          )}
          {/* Shown only on rows that have been sitting: for anything touched today the sort
              order already says so, and it is not worth 23% of the title (measured: 38px of
              130px). */}
          {when && (
            <span className="wi-when" title={fullLocal(item.updatedAt)}>
              {when}
            </span>
          )}
        </div>
        {meta && (
          <div className="wi-meta" title={labels.length ? labels.join(", ") : undefined}>
            {repo && <span className="wi-repo">{repo}</span>}
            {assignee && <span className="wi-assignee">@{assignee}</span>}
            {labels.map((l) => (
              <LabelBadge key={l} name={l} color={item.labelColors[l]} />
            ))}
          </div>
        )}
      </div>
      {/* The started badge. This is the ledger's main payoff: it stops a second person picking
          up the same ticket before they launch (docs/log/80 §80.8). It stays on the row because
          it is information — someone already holds this row — not a control. */}
      {busy && (
        <button
          type="button"
          className="wi-started"
          title={tr("wi.started_at", { name: startedName })}
          onClick={(e) => {
            e.stopPropagation();
            onOpenSession(started[0].sessionName);
          }}
        >
          <Icon name="circle-filled" />
          {started.length > 1 ? started.length : ""}
        </button>
      )}
      {/* Going straight to the tracker stays on the row: it is the lightest action there is
          and does not go through af at all. */}
      <a
        className="wi-link"
        href={item.url}
        target="_blank"
        rel="noreferrer noopener"
        title={tr("wi.open_external")}
        onClick={(e) => e.stopPropagation()}
      >
        <Icon name="link-external" />
      </a>
    </div>
  );
});

export const WorkItemsSection = memo(function WorkItemsSection() {
  const tr = useT();
  const toast = useToast();
  const tenant = useTenantStore((s) => s.tenant);
  const payload = useWorkItemStore((s) => s.payload);
  const loaded = useWorkItemStore((s) => s.loaded);
  const loadErr = useWorkItemStore((s) => s.loadErr);
  const refreshing = useWorkItemStore((s) => s.refreshing);
  const reset = useWorkItemStore((s) => s.reset);
  const sessions = useSessionsStore((s) => s.sessions);
  // The detail modal is rendered by WorkItemModalHost, so a ticket link elsewhere opens the same
  // single instance (#1659).
  const openDetail = useWorkItemModal((s) => s.openDetail);
  const openSession = useOpenWorkItemSession();
  const [queries, setQueries] = useState(false);
  const [needle, setNeedle] = useState("");
  // The tracker search answers the needle it was pressed for; typing on makes it stale, so it
  // is dropped rather than shown under a filter it no longer matches.
  const [remote, setRemote] = useState<{ needle: string; result: WorkItemSearchResult } | null>(null);
  const [remoteBusy, setRemoteBusy] = useState(false);
  const [remoteErr, setRemoteErr] = useState("");
  // Bumped whenever the needle changes, so a search still in flight cannot bring back an error
  // for a needle the user has already typed over or cleared.
  const searchGen = useRef(0);
  const filterInput = useRef<HTMLInputElement>(null);
  // Switching tenant must not leave the previous tenant's rows behind (as in the other stores).
  useEffect(() => {
    reset();
  }, [tenant, reset]);
  useEffect(() => startWorkItemPolling(), [tenant]);

  // Sort first, then dedupe to one row per ticket (docs/log/80 §80.20). Deduping after the sort
  // is what makes the surviving row the one that heads the shelf: still open and most recent.
  const items = useMemo(() => dedupeWorkItems(sortWorkItems(payload?.items || [])), [payload]);
  const ledger = payload?.sessions || [];

  // Rows seen for the first time get their current state as the baseline, so the first load (or a
  // newly listed ticket) is not drawn as unread; only a later move past it is.
  const seen = useSeenStore((s) => s.map);
  useEffect(() => {
    if (payload) useSeenStore.getState().seed(payload.items);
  }, [payload]);

  // The volume wall (measured at 41 rows, docs/log/80 §80.18.4): past it the filter box appears.
  // The filter is display only and the payload is kept whole, because a stopped workspace cannot
  // be asked for more.
  const uniform = useMemo(() => uniformMeta(items), [items]);
  const matched = useMemo(() => items.filter((i) => matchWorkItem(i, needle)), [items, needle]);
  const crowded = items.length > RAIL_VISIBLE;

  const q = needle.trim();
  const truncated = !!payload?.queries.some((x) => x.enabled && x.matchTotal !== 0);
  const canSearch = !!q && (matched.length === 0 || truncated);
  const remoteRows = useMemo(() => {
    if (!remote || remote.needle !== q) return null;
    const onRail = new Set(items.map((i) => `${i.provider}:${i.key}`));
    return remote.result.items.filter((i) => !onRail.has(`${i.provider}:${i.key}`));
  }, [remote, q, items]);
  const searchTracker = () => {
    if (!canSearch || remoteBusy || !payload?.running) return;
    const asked = q;
    const gen = searchGen.current;
    setRemoteBusy(true);
    setRemoteErr("");
    void workItemSearch(asked)
      .then((res) => {
        if (gen !== searchGen.current) return;
        const got = readWorkItemSearch(res);
        if (got.result) setRemote({ needle: asked, result: got.result });
        else setRemoteErr(errText(got.error) || tr("wi.search_failed"));
      })
      .finally(() => setRemoteBusy(false));
  };
  const changeNeedle = (next: string) => {
    searchGen.current++;
    setNeedle(next);
    setRemoteErr("");
  };
  const labelOf = (id: string) => payload?.queries.find((x) => x.id === id)?.label || id;

  const startedNameFor = (item: WorkItem) => {
    const first = sessionsForItem(ledger, item.key)[0];
    return first ? resolveSessionRef(first.sessionName, sessions, null).title || first.sessionName : "";
  };

  const count = items.filter((i) => i.state !== "done").length;
  const unreadCount = items.filter((i) => unreadOf(seen, i) !== "").length;
  const stamp = shortLocal(payload?.fetchedAt || "");

  return (
    <Section
      id="workitems"
      title={tr("wi.title")}
      icon="issues"
      count={count}
      actions={
        <>
          <IconButton
            icon="refresh"
            label={tr("wi.refresh")}
            spin={refreshing}
            onClick={(e) => {
              e.stopPropagation();
              void useWorkItemStore.getState().forceRefresh();
            }}
          />
          <IconButton
            icon="settings-gear"
            label={tr("wi.queries")}
            onClick={(e) => {
              e.stopPropagation();
              setQueries(true);
            }}
          />
        </>
      }
    >
      {/* Always say when this was fetched. Fetching stops while the workspace is stopped, and
          the one thing this must never do is show a possibly stale list without saying so
          (ADR 0061 decision 1). */}
      <div className="wi-stamp">
        {stamp ? tr("wi.fetched_at", { at: stamp }) : tr("wi.never_fetched")}
        {unreadCount > 0 && <span className="wi-unread-total">{tr("wi.unread_total", { n: unreadCount })}</span>}
        {payload && !payload.running && <span className="wi-stopped">{tr("wi.stopped_note")}</span>}
      </div>
      {loadErr && (
        <div className="wi-err" role="status" title={loadErr}>
          <Icon name="warning" />
          <span>{tr("wi.load_failed")}</span>
        </div>
      )}
      {payload?.queries
        .filter((q) => q.enabled && q.lastError)
        .map((q) => (
          <div className="wi-err" key={q.id} role="status" title={q.lastError}>
            <Icon name="warning" />
            <span>{tr("wi.query_failed", { label: q.label })}</span>
          </div>
        ))}
      {/* A query that matched more than one page carries says so (#1095). Without this the header
          count reads as "everything" and a ticket past the cut is simply not there, which the
          filter box then confirms. */}
      {payload?.queries
        .filter((q) => q.enabled && !q.lastError && q.matchTotal !== 0)
        .map((q) => {
          const shown = payload.items.filter((i) => i.queryId === q.id).length;
          const msg =
            q.matchTotal > 0
              ? tr("wi.query_truncated", { label: q.label, shown, total: q.matchTotal })
              : tr("wi.query_truncated_unknown", { label: q.label, shown });
          return (
            <div className="wi-trunc" key={q.id} role="status" title={msg}>
              {msg}
            </div>
          );
        })}
      {loaded && !payload?.queries.length ? (
        <div className="pane-empty">
          {tr("wi.no_queries")}
          <button type="button" className="wi-add-first" onClick={() => setQueries(true)}>
            {tr("wi.add_query")}
          </button>
        </div>
      ) : loaded && items.length === 0 ? (
        <div className="pane-empty">{tr("wi.empty")}</div>
      ) : (
        <>
          {/* One line, shown only on a crowded rail. It never reaches the provider, is never
              saved and does not reorder — it only helps the eye find a row among those already
              on screen (§80.18.4). */}
          {crowded && (
            <div className="wi-filter">
              <div className="proj-filter">
                <Icon name="search" />
                <input {...NO_AUTOFILL}
                  ref={filterInput}
                  type="search"
                  value={needle}
                  placeholder={tr("wi.filter_ph")}
                  aria-label={tr("wi.filter_ph")}
                  onChange={(e) => changeNeedle(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") searchTracker();
                    else if (e.key === "Escape") changeNeedle("");
                  }}
                />
                {needle && (
                  <button
                    type="button"
                    className="proj-filter-clear"
                    title={tr("pj.clear")}
                    onClick={(e) => {
                      changeNeedle("");
                      // The button unmounts with the needle, dropping keyboard focus to <body>. Only a
                      // keyboard press (detail 0) gets it back: on a tap, focusing the input would pop
                      // the soft keyboard over the rows the user cleared the filter to see.
                      if (e.detail === 0) filterInput.current?.focus();
                    }}
                  >
                    <Icon name="close" />
                  </button>
                )}
              </div>
            </div>
          )}
          <div className="wi-list">
            {matched.map((item) => (
              <WorkItemRow
                key={item.id}
                item={item}
                started={sessionsForItem(ledger, item.key)}
                startedName={startedNameFor(item)}
                uniform={uniform[item.queryId] || { repo: false, assignee: false }}
                unread={unreadOf(seen, item)}
                onOpen={openDetail}
                onOpenSession={openSession}
              />
            ))}
          </div>
          {matched.length === 0 && <div className="pane-empty">{tr("wi.filter_empty")}</div>}
          {/* Past the rail's cut (#1095). Offered when the filter found nothing or a query's page
              is known to be partial; a press, never a keystroke. */}
          {canSearch && !remoteRows && (
            <button
              type="button"
              className="wi-search"
              disabled={remoteBusy || !payload?.running}
              title={payload?.running ? undefined : tr("wi.search_stopped")}
              onClick={searchTracker}
            >
              {remoteBusy ? tr("wi.searching") : tr("wi.search_tracker", { q })}
            </button>
          )}
          {canSearch && !payload?.running && <div className="wi-trunc">{tr("wi.search_stopped")}</div>}
          {remoteErr && (
            <div className="wi-err" role="status" title={remoteErr}>
              <Icon name="warning" />
              <span>{remoteErr}</span>
            </div>
          )}
          {remoteRows && remote && (
            <div className="wi-remote">
              <div className="wi-remote-head">{tr("wi.search_head", { q: remote.needle })}</div>
              {remoteRows.map((item) => (
                <WorkItemRow
                  key={item.id}
                  item={item}
                  started={sessionsForItem(ledger, item.key)}
                  startedName={startedNameFor(item)}
                  uniform={{ repo: false, assignee: false }}
                  unread=""
                  onOpen={openDetail}
                  onOpenSession={openSession}
                />
              ))}
              {remoteRows.length === 0 && remote.result.errors.length === 0 && (
                <div className="pane-empty">{tr("wi.search_none")}</div>
              )}
              {remote.result.errors.map((e) => (
                <div className="wi-err" key={e.queryId} role="status" title={e.message}>
                  <Icon name="warning" />
                  <span>{tr("wi.query_failed", { label: labelOf(e.queryId) })}</span>
                </div>
              ))}
              {remote.result.skipped.length > 0 && (
                <div className="wi-trunc">
                  {tr("wi.search_skipped", { labels: remote.result.skipped.map(labelOf).join(" / ") })}
                </div>
              )}
            </div>
          )}
        </>
      )}
      {queries && (
        <WorkItemQueryModal
          queries={payload?.queries || []}
          onClose={() => setQueries(false)}
          onChanged={() => {
            void useWorkItemStore.getState().refresh();
          }}
          onSaved={() => toast(t("wi.query_saved"), { kind: "success" })}
        />
      )}
    </Section>
  );
});
