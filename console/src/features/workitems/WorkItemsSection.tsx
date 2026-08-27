// WorkItemsSection (docs/80 P0) — the left-rail inbox of external tickets (GitHub Issue
// and PR; Jira in P1). Membership-scoped and Control-Plane persisted like the memo queue,
// so it renders in BOTH the running and the stopped rail — picking a ticket happens
// before a session exists, which is exactly when the Workspace tends to be stopped.
//
// ★ This is not a ticket viewer (docs/80 §80.1). No query builder, no detail pane, no
// sort control: composing what to fetch is the saved query's job, and the row's job is to
// start a session with the ticket's context already in place. Anything more is a worse
// copy of the tracker's own web UI.
//
// ⚠️ The line moved once, on real data (docs/80 §80.18 / ADR 0061 decision 14). One saved
// query returned 41 rows, so the rail folds at RAIL_VISIBLE and offers a one-line filter
// over the rows it already has. Neither touches the provider, is saved, or reorders —
// that is the whole distinction between "the rail's job" and "the query's job".
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
import { useReposStore, type Repo } from "../repos/store.ts";
import { useLaunchSeed, useLaunchTarget } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettings } from "../../lib/settings.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { useWorkItemStore, startWorkItemPolling } from "./store.ts";
import { WorkItemQueryModal } from "./WorkItemQueryModal.tsx";
import { WorkItemReportModal } from "./WorkItemReportModal.tsx";
import { WorkItemStartModal } from "./WorkItemStartModal.tsx";
import {
  branchForItem,
  fullLocal,
  matchWorkItem,
  promptForItem,
  RAIL_VISIBLE,
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
  type WorkItem,
  type WorkItemSessionRef,
} from "./read.ts";
import "./workitems.css";

interface RowProps {
  item: WorkItem;
  started: WorkItemSessionRef[];
  /** Meta this query repeats on every row — dropped from the line (docs/80 §80.18.2). */
  uniform: { repo: boolean; assignee: boolean };
  onStart(item: WorkItem): void;
  onOpenSession(name: string): void;
  onReport(item: WorkItem): void;
}

const WorkItemRow = memo(function WorkItemRow({ item, started, uniform, onStart, onOpenSession, onReport }: RowProps) {
  const tr = useT();
  const tone = stateTone(item.state);
  const busy = started.length > 0;
  // 行に出すのは「行ごとに違うもの」だけ（docs/80 §80.18.2）。全行で同じ担当者 /
  // リポジトリは落とし、★ 残りが無ければ 2 行目そのものを描かない —— Jira の既定
  // クエリはここで 1 行になり、レールの縦が半分になる。空いた高さは埋め直さない。
  const repo = uniform.repo ? "" : item.repo;
  const assignee = uniform.assignee ? "" : item.assignee;
  const labels = item.labels.slice(0, 2);
  const meta = !!(repo || assignee || labels.length);
  const when = railWhen(item.updatedAt);
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
          <span className="wi-title" title={item.assignee ? `${item.title} — @${item.assignee}` : item.title}>
            {item.title}
          </span>
          {/* ★ 放置されている行にだけ出す。今日動いた行では並び順が既にそれを言って
              いるので、タイトルの 23%（実測 38px / 130px）を払う価値がない。 */}
          {when && (
            <span className="wi-when" title={fullLocal(item.updatedAt)}>
              {when}
            </span>
          )}
        </div>
        {meta && (
          <div className="wi-meta">
            {repo && <span className="wi-repo">{repo}</span>}
            {assignee && <span className="wi-assignee">@{assignee}</span>}
            {labels.map((l) => (
              <span className="wi-label" key={l}>
                {l}
              </span>
            ))}
          </div>
        )}
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
  const [startOn, setStartOn] = useState<WorkItem | null>(null);
  const [needle, setNeedle] = useState("");
  const [expanded, setExpanded] = useState(false);

  // テナントを切り替えたら前のテナントの行を残さない（他のストアと同じ作法）。
  useEffect(() => {
    reset();
  }, [tenant, reset]);
  useEffect(() => startWorkItemPolling(), [tenant]);

  const items = useMemo(() => sortWorkItems(payload?.items || []), [payload]);
  const ledger = payload?.sessions || [];
  const folders = useMemo(() => repos.map((r) => r.name), [repos]);

  // 量の壁（実測 41 件・docs/80 §80.18.4）。絞り込んでから畳む: 検索窓に打った人が
  // 見たいのは「絞った結果の上位 10 件」であって「上位 10 件の中の一致」ではない。
  // ★ 畳むのは表示だけで payload は丸ごと持ったまま —— 停止中は取りに行けないから。
  const uniform = useMemo(() => uniformMeta(items), [items]);
  const matched = useMemo(() => items.filter((i) => matchWorkItem(i, needle)), [items, needle]);
  const crowded = items.length > RAIL_VISIBLE;
  const shown = expanded || !crowded ? matched : matched.slice(0, RAIL_VISIBLE);
  const hidden = matched.length - shown.length;

  const openSession = (name: string) => {
    const s = sessions.find((x) => x.name === name);
    (agentOf(s?.kind || "claude").caps.chat ? openSessionChat : openSessionTerminal)(name);
  };

  // 始める: まず「どこで」を聞く（docs/80 §80.8）。チケットは作業コピーを知らない —
  // GitHub 項目はリポジトリまで、Jira はそれすら持たない — ので、リポジトリと
  // 新規 worktree / 既存コピー を選んでから既存の起動スタックへ渡す。
  // 作業コピーが 1 つも無いときだけ はじめる ハブ（clone 導線）に委ねる。
  const start = (item: WorkItem) => {
    if (!repos.some((r) => !r.worktree)) {
      seedFor(item);
      startHub();
      return;
    }
    setStartOn(item);
  };

  const seedFor = (item: WorkItem) => {
    seed(promptForItem(item), titleForItem(item), "", "", "", {
      provider: item.provider,
      key: item.key,
      branch: branchForItem(item, settings.workItemBranchTemplate),
    });
  };

  const pickTarget = (item: WorkItem, target: Repo, inPlace: boolean) => {
    seedFor(item);
    setStartOn(null);
    openLaunch(target, "", inPlace);
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
        <>
          {/* 混んでいるレールにだけ出す 1 行。provider を叩かず・保存せず・並び順も
              変えない —— いま出ている行の中を目で探すのを助けるだけ（§80.18.4）。 */}
          {crowded && (
            <div className="wi-filter">
              <Icon name="search" />
              <input
                type="search"
                value={needle}
                placeholder={tr("wi.filter_ph")}
                aria-label={tr("wi.filter_ph")}
                onChange={(e) => setNeedle(e.target.value)}
              />
            </div>
          )}
          <div className="wi-list">
            {shown.map((item) => (
              <WorkItemRow
                key={item.id}
                item={item}
                started={sessionsForItem(ledger, item.key)}
                uniform={uniform[item.queryId] || { repo: false, assignee: false }}
                onStart={start}
                onOpenSession={openSession}
                onReport={setReportOn}
              />
            ))}
          </div>
          {matched.length === 0 && <div className="pane-empty">{tr("wi.filter_empty")}</div>}
          {/* 残りの件数は必ず数で書く。バッジは全件のままなので、ここが「隠していない」
              ことの説明になっている。 */}
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
      {startOn && (
        <WorkItemStartModal
          item={startOn}
          repos={repos}
          defaultRepo={repoForItem(startOn, payload?.queries.find((q) => q.id === startOn.queryId)?.repoHint || "", folders)}
          onClose={() => setStartOn(null)}
          onPick={(target, inPlace) => pickTarget(startOn, target, inPlace)}
        />
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
