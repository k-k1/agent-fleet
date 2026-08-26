// WorkItemsSection (docs/80 P0) — the left-rail inbox of external tickets (GitHub Issue
// and PR; Jira in P1). Membership-scoped and Control-Plane persisted like the memo queue,
// so it renders in BOTH the running and the stopped rail — picking a ticket happens
// before a session exists, which is exactly when the Workspace tends to be stopped.
//
// ★ This is not a ticket viewer (docs/80 §80.1). There is no filtering UI, no detail
// pane, no sort control: the saved query is the filter, and the row's job is to start a
// session with the ticket's context already in place. Anything more is a worse copy of
// the tracker's own web UI.
//
// The row's primary action opens the existing launch stack (seed → StartHost) rather than
// a dialog of its own, so worktree/branch/agent behaviour stays in one place.
import { memo, useEffect, useMemo, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useReposStore } from "../repos/store.ts";
import { useLaunchSeed, useLaunchTarget } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettings } from "../../lib/settings.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { useWorkItemStore, startWorkItemPolling } from "./store.ts";
import { WorkItemQueryModal } from "./WorkItemQueryModal.tsx";
import { WorkItemReportModal } from "./WorkItemReportModal.tsx";
import {
  branchForItem,
  promptForItem,
  repoForItem,
  sessionsForItem,
  shortKey,
  shortLocal,
  sortWorkItems,
  stateLabel,
  stateTone,
  titleForItem,
  type WorkItem,
  type WorkItemSessionRef,
} from "./read.ts";
import "./workitems.css";

interface RowProps {
  item: WorkItem;
  started: WorkItemSessionRef[];
  onStart(item: WorkItem): void;
  onOpenSession(name: string): void;
  onReport(item: WorkItem): void;
}

const WorkItemRow = memo(function WorkItemRow({ item, started, onStart, onOpenSession, onReport }: RowProps) {
  const tr = useT();
  const tone = stateTone(item.state);
  const busy = started.length > 0;
  return (
    <div className={"wi-row" + (item.state === "done" ? " done" : "")}>
      <span className={`wi-dot tone-${tone}`} title={stateLabel(item.state)}>
        <Icon name={item.kind === "pr" ? "git-pull-request" : "issues"} />
      </span>
      <div className="wi-info">
        <div className="wi-head">
          <span className="wi-key" title={item.key}>
            {shortKey(item.key)}
          </span>
          <span className="wi-title" title={item.title}>
            {item.title}
          </span>
        </div>
        <div className="wi-meta">
          {item.repo && <span className="wi-repo">{item.repo}</span>}
          {item.assignee && <span className="wi-assignee">@{item.assignee}</span>}
          {item.labels.slice(0, 2).map((l) => (
            <span className="wi-label" key={l}>
              {l}
            </span>
          ))}
        </div>
      </div>
      {/* 着手済みバッジ。台帳の一番の実利がこれ —— 同じ課題に 2 人目が入るのを、
          起動する前に止める（docs/80 §80.8）。 */}
      {busy && (
        <button
          type="button"
          className="wi-started"
          title={tr("wi.started_at", { name: started[0].sessionName })}
          onClick={() => onOpenSession(started[0].sessionName)}
        >
          <Icon name="circle-filled" />
          {started.length > 1 ? started.length : ""}
        </button>
      )}
      {/* 書き戻しは着手した行にだけ出す。押しても投稿はされない —— 下書きを読む
          モーダルが開くだけで、投稿はその中の 1 手（ADR 0061 決定 6）。 */}
      {busy && (
        <button type="button" className="wi-report" onClick={() => onReport(item)} title={tr("wi.report_title")}>
          <Icon name="comment" />
        </button>
      )}
      <a className="wi-link" href={item.url} target="_blank" rel="noreferrer noopener" title={tr("wi.open_external")}>
        <Icon name="link-external" />
      </a>
      <button type="button" className="wi-start" onClick={() => onStart(item)} title={tr("wi.start")}>
        {tr("wi.start")}
      </button>
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
  const repos = useReposStore((s) => s.repos);
  const settings = useSettings();
  const sessions = useSessionsStore((s) => s.sessions);
  const seed = useLaunchSeed((s) => s.set);
  const openLaunch = useLaunchTarget((s) => s.open);
  const startHub = useSessionsStore((s) => s.openStart);
  const [queries, setQueries] = useState(false);
  const [reportOn, setReportOn] = useState<WorkItem | null>(null);

  // テナントを切り替えたら前のテナントの行を残さない（他のストアと同じ作法）。
  useEffect(() => {
    reset();
  }, [tenant, reset]);
  useEffect(() => startWorkItemPolling(), [tenant]);

  const items = useMemo(() => sortWorkItems(payload?.items || []), [payload]);
  const ledger = payload?.sessions || [];
  const folders = useMemo(() => repos.map((r) => r.name), [repos]);

  const openSession = (name: string) => {
    const s = sessions.find((x) => x.name === name);
    (agentOf(s?.kind || "claude").caps.chat ? openSessionChat : openSessionTerminal)(name);
  };

  // 始める: 既存の起動スタックに種を置いて渡す。作業コピーが分かるなら
  // LaunchModal を直接、分からないなら はじめる ハブに選ばせる。
  const start = (item: WorkItem) => {
    const hint = payload?.queries.find((q) => q.id === item.queryId)?.repoHint || "";
    const folder = repoForItem(item, hint, folders);
    seed(promptForItem(item), titleForItem(item), "", "", "", {
      provider: item.provider,
      key: item.key,
      branch: branchForItem(item, settings.workItemBranchTemplate),
    });
    const repo = repos.find((r) => r.name === folder);
    if (repo) openLaunch(repo);
    else startHub();
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
      {/* 「いつ取ったか」は必ず出す。停止中は取得が止まるので、古いかもしれない
          一覧を古いと言わずに出すことだけはしない（ADR 0061 決定 1）。 */}
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
        <div className="wi-list">
          {items.map((item) => (
            <WorkItemRow
              key={item.id}
              item={item}
              started={sessionsForItem(ledger, item.key)}
              onStart={start}
              onOpenSession={openSession}
              onReport={setReportOn}
            />
          ))}
        </div>
      )}
      {reportOn && (
        <WorkItemReportModal
          item={reportOn}
          sessions={sessionsForItem(ledger, reportOn.key)}
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
