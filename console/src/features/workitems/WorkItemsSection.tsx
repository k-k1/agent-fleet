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
// query returned 41 rows, so the rail folds at RAIL_VISIBLE and offers a one-line filter
// over the rows it already has. Neither touches the provider, is saved, or reorders —
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
import { memo, useEffect, useMemo, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, raw } from "../../core/api/client.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useReposStore, type Repo } from "../repos/store.ts";
import { useLaunchSeed, useLaunchTarget } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettings } from "../../lib/settings.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { useWorkItemStore, startWorkItemPolling } from "./store.ts";
import { WorkItemQueryModal } from "./WorkItemQueryModal.tsx";
import { WorkItemReportModal } from "./WorkItemReportModal.tsx";
import { WorkItemDetailModal } from "./WorkItemDetailModal.tsx";
import { LabelBadge } from "./LabelBadge.tsx";
import { resolveSessionRef, useArchivedFor, type ResolvedSessionRef } from "./sessionRefs.ts";
import {
  branchForItem,
  dedupeWorkItems,
  fullLocal,
  matchWorkItem,
  promptForItem,
  RAIL_VISIBLE,
  railLabels,
  railWhen,
  repoForItem,
  sessionsForItem,
  shortKey,
  shortLocal,
  sortWorkItems,
  stateLabel,
  stateTone,
  titleForItem,
  uniformMeta,
  readWorkItemSearch,
  type WorkItem,
  type WorkItemSearchResult,
  type WorkItemSessionRef,
} from "./read.ts";
import { workItemSearch } from "./api.ts";
import { errText } from "../../core/api/client.ts";
import "./workitems.css";

interface RowProps {
  item: WorkItem;
  started: WorkItemSessionRef[];
  /** What the started badge calls the first session: its display name when it is on the live
   * list, else its slug (#1108). */
  startedName: string;
  /** Meta this query repeats on every row — dropped from the line (docs/log/80 §80.18.2). */
  uniform: { repo: boolean; assignee: boolean };
  onOpen(item: WorkItem): void;
  onOpenSession(name: string): void;
}

const WorkItemRow = memo(function WorkItemRow({ item, started, startedName, uniform, onOpen, onOpenSession }: RowProps) {
  const tr = useT();
  const tone = stateTone(item.state);
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
  return (
    // The whole row opens the detail modal. The external link and the started badge nested
    // inside it are controls of their own, so each stops propagation before acting; otherwise
    // one press would open two things.
    <div
      className={"wi-row" + (item.state === "done" ? " done" : "")}
      role="button"
      tabIndex={0}
      aria-label={tr("wi.open_detail", { key: item.key })}
      onClick={() => onOpen(item)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen(item);
        }
      }}
    >
      <span className={`wi-dot tone-${tone}`} title={stateLabel(item.state)}>
        <Icon name={item.kind === "pr" ? "git-pull-request" : "issues"} />
      </span>
      <div className="wi-info">
        <div className="wi-head">
          <span className="wi-key" title={item.key}>
            {shortKey(item.key)}
          </span>
          <span className="wi-title" title={item.assignee ? `${item.title} — @${item.assignee}` : item.title}>
            {item.title}
          </span>
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
  const askConfirm = useConfirm();
  const tenant = useTenantStore((s) => s.tenant);
  const payload = useWorkItemStore((s) => s.payload);
  const loaded = useWorkItemStore((s) => s.loaded);
  const loadErr = useWorkItemStore((s) => s.loadErr);
  const refreshing = useWorkItemStore((s) => s.refreshing);
  const reset = useWorkItemStore((s) => s.reset);
  const repos = useReposStore((s) => s.repos);
  const settings = useSettings();
  const sessions = useSessionsStore((s) => s.sessions);
  const seed = useLaunchSeed((s) => s.set);
  const openLaunch = useLaunchTarget((s) => s.open);
  const startHub = useSessionsStore((s) => s.openStart);
  const [queries, setQueries] = useState(false);
  const [reportOn, setReportOn] = useState<WorkItem | null>(null);
  const [detailOn, setDetailOn] = useState<WorkItem | null>(null);
  const [needle, setNeedle] = useState("");
  const [expanded, setExpanded] = useState(false);
  // The tracker search answers the needle it was pressed for; typing on makes it stale, so it
  // is dropped rather than shown under a filter it no longer matches.
  const [remote, setRemote] = useState<{ needle: string; result: WorkItemSearchResult } | null>(null);
  const [remoteBusy, setRemoteBusy] = useState(false);
  const [remoteErr, setRemoteErr] = useState("");

  // Switching tenant must not leave the previous tenant's rows behind (as in the other stores).
  useEffect(() => {
    reset();
  }, [tenant, reset]);
  useEffect(() => startWorkItemPolling(), [tenant]);

  // Sort first, then dedupe to one row per ticket (docs/log/80 §80.20). Deduping after the sort
  // is what makes the surviving row the one that heads the shelf: still open and most recent.
  const items = useMemo(() => dedupeWorkItems(sortWorkItems(payload?.items || [])), [payload]);
  const ledger = payload?.sessions || [];
  const folders = useMemo(() => repos.map((r) => r.name), [repos]);

  // The volume wall (measured at 41 rows, docs/log/80 §80.18.4). Filter first, then fold: what
  // someone typing in the box wants is the top 10 of the filtered result, not the matches within
  // the top 10. Folding is display only and the payload is kept whole, because a stopped
  // workspace cannot be asked for more.
  const uniform = useMemo(() => uniformMeta(items), [items]);
  const matched = useMemo(() => items.filter((i) => matchWorkItem(i, needle)), [items, needle]);
  const crowded = items.length > RAIL_VISIBLE;
  const shown = expanded || !crowded ? matched : matched.slice(0, RAIL_VISIBLE);
  const hidden = matched.length - shown.length;

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
    setRemoteBusy(true);
    setRemoteErr("");
    void workItemSearch(asked)
      .then((res) => {
        const got = readWorkItemSearch(res);
        if (got.result) setRemote({ needle: asked, result: got.result });
        else setRemoteErr(errText(got.error) || tr("wi.search_failed"));
      })
      .finally(() => setRemoteBusy(false));
  };
  const labelOf = (id: string) => payload?.queries.find((x) => x.id === id)?.label || id;

  // The ledger names a slug; what the modals show for it is looked up here (#1108). The shelf is
  // read only while a modal lists a slug that is not on the live list.
  const shownRefs = [detailOn, reportOn].flatMap((i) => (i ? sessionsForItem(ledger, i.key) : []));
  const missing = [...new Set(shownRefs.map((r) => r.sessionName))].filter((n) => !sessions.some((s) => s.name === n));
  const archived = useArchivedFor(missing);
  const sessionRef = (name: string): ResolvedSessionRef => resolveSessionRef(name, sessions, archived);

  const startedNameFor = (item: WorkItem) => {
    const first = sessionsForItem(ledger, item.key)[0];
    return first ? sessionRef(first.sessionName).title || first.sessionName : "";
  };

  const openLive = (name: string) => {
    const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
    (agentOf(s?.kind || "claude").caps.chat ? openSessionChat : openSessionTerminal)(name);
  };

  // A slug that is not on the live list used to open nothing at all (#1108). Read the shelf
  // fresh at the click — the row badge reaches here with no modal open — and offer to bring an
  // archived session back; a slug on neither list is said to be gone. If the shelf cannot be
  // read (a stopped workspace), fall through to the plain open, which is what it always did.
  const openSession = async (name: string) => {
    if (sessions.some((s) => s.name === name)) return openLive(name);
    let shelf: ResolvedSessionRef;
    try {
      const d = await api("api/sessions/archived");
      shelf = resolveSessionRef(name, [], d.sessions || []);
    } catch {
      return openLive(name);
    }
    if (shelf.state === "gone") {
      toast(t("wi.session_gone", { name }));
      return;
    }
    const ok = await askConfirm({
      title: tr("wi.restore_title"),
      body: tr("wi.restore_body", { name: shelf.title || name }),
      confirmLabel: tr("arch.restore"),
      danger: false,
    });
    if (!ok) return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/restore`, { method: "POST" }).catch(() => null);
    if (!res?.ok) {
      toast(t("arch.restore_failed"));
      return;
    }
    await useSessionsStore.getState().refresh();
    openLive(name);
  };

  // reviewBranch: the PR's head branch, when the detail modal's live read resolved one and the
  // launch is landing in a fresh worktree (WorkItemDetailModal only sets it in that case).
  const seedFor = (item: WorkItem, reviewBranch = "") => {
    seed(promptForItem(item, undefined, reviewBranch), titleForItem(item), "", "", "", {
      provider: item.provider,
      key: item.key,
      branch: branchForItem(item, settings.workItemBranchTemplate),
    });
  };

  // Hand off to the existing launch stack only once the detail modal has settled WHERE
  // (docs/log/80 §80.8). A ticket knows nothing about working copies — a GitHub item names a
  // repository at most, Jira not even that — so the repository and new-worktree vs. existing-copy
  // choice are already decided by the time this runs.
  //
  // reviewBranch, when set, checks that branch out in the new worktree instead of cutting one
  // from the template (docs/log/80 §80.24) — reviewing a pull request means reading the code
  // it already has, not starting a new branch from it. It is dropped for inPlace: the user
  // picked that existing copy by hand, and launching it on a DIFFERENT branch than the one they
  // saw in the picker would be a silent switch under them.
  const pickTarget = (item: WorkItem, target: Repo, inPlace: boolean, reviewBranch: string) => {
    seedFor(item, reviewBranch);
    setDetailOn(null);
    openLaunch(target, inPlace ? "" : reviewBranch, inPlace);
  };

  // Defer to the start hub (the clone path) only when there is no working copy at all.
  const toStartHub = (item: WorkItem) => {
    seedFor(item);
    setDetailOn(null);
    startHub();
  };

  const count = items.filter((i) => i.state !== "done").length;
  const stamp = shortLocal(payload?.fetchedAt || "");

  return (
    <Section
      id="workitems"
      title={tr("wi.title")}
      icon="tasklist"
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
              <Icon name="search" />
              <input
                type="search"
                value={needle}
                placeholder={tr("wi.filter_ph")}
                aria-label={tr("wi.filter_ph")}
                onChange={(e) => {
                  setNeedle(e.target.value);
                  setRemoteErr("");
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter") searchTracker();
                }}
              />
            </div>
          )}
          <div className="wi-list">
            {shown.map((item) => (
              <WorkItemRow
                key={item.id}
                item={item}
                started={sessionsForItem(ledger, item.key)}
                startedName={startedNameFor(item)}
                uniform={uniform[item.queryId] || { repo: false, assignee: false }}
                onOpen={setDetailOn}
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
                  onOpen={setDetailOn}
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
          {/* Always name the remaining count. The section badge still counts everything, so this
              line is what explains that nothing is being hidden. */}
          {hidden > 0 && (
            <button type="button" className="wi-more" onClick={() => setExpanded(true)}>
              {tr("wi.show_more", { n: hidden })}
            </button>
          )}
          {expanded && crowded && (
            <button type="button" className="wi-more" onClick={() => setExpanded(false)}>
              {tr("wi.show_less")}
            </button>
          )}
        </>
      )}
      {detailOn && (
        <WorkItemDetailModal
          item={detailOn}
          repos={repos}
          defaultRepo={repoForItem(detailOn, payload?.queries.find((q) => q.id === detailOn.queryId)?.repoHint || "", folders)}
          started={sessionsForItem(ledger, detailOn.key)}
          onClose={() => setDetailOn(null)}
          onPick={(target, inPlace, reviewBranch) => pickTarget(detailOn, target, inPlace, reviewBranch)}
          onStartHub={() => toStartHub(detailOn)}
          sessionRef={sessionRef}
          onOpenSession={(name) => {
            setDetailOn(null);
            void openSession(name);
          }}
          // Close the detail modal before opening the report one: never stack two modals, since
          // both the Esc layering and the focus trap assume one at a time.
          onReport={() => {
            setDetailOn(null);
            setReportOn(detailOn);
          }}
        />
      )}
      {reportOn && (
        <WorkItemReportModal
          item={reportOn}
          sessions={sessionsForItem(ledger, reportOn.key)}
          sessionRef={sessionRef}
          onClose={() => setReportOn(null)}
        />
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
