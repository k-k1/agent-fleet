// Work item detail (docs/log/80 §80.8 / §80.20).
//
// The single panel a rail row opens. The row lost its "start" button because forty-one identical
// buttons down the right edge make the rail look like a surface where pressing does something
// (§80.20); the row is information again and the controls live here.
//
// For an issue it shows the fields the CP holds (key/title/state/url/assignee/labels/repo/updated)
// and nothing else. For a PULL REQUEST it also reads the pull request live when it opens
// (§80.24): draft, conflicts, reviews and CI decide whether to pick a review up, and all four
// change between two rail refreshes, so a cached copy of them would be worse than none.
//
// It still does NOT show the body. The CP is not allowed to hold ticket bodies (ADR 0061
// decision 2) and the live read does not fetch one either — it is read inside the session by
// `gh` or the MCP (§80.9). Nothing the live read returns is stored, here or in the CP.
//
// Choosing where to launch (repository, new worktree vs. existing working copy) is folded in here.
// A ticket does not know where its work happens: a GitHub item names a repository but
// not which local copy, and a Jira issue names neither.
//
// Why this is not the start hub: that hub deliberately lists BASE clones only
// ("worktrees are task copies, launched from their tree rows"), and continuing a ticket
// in the worktree it already has is the normal second visit. It is also why this is not
// left to the launch dialog's location section, which offers a new worktree or this copy
// directly but cannot point at a DIFFERENT existing copy.
//
// The answer is handed to the existing LaunchModal (via useLaunchTarget) rather than
// re-implementing any of it — agent, model, prompt, branch and worktree creation all
// stay in one place.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import type { Repo } from "../repos/store.ts";
import { workItemDetail } from "./api.ts";
import {
  canComment,
  canReadLive,
  checksTone,
  fullLocal,
  readWorkItemDetail,
  relTime,
  reviewCounts,
  stateLabel,
  stateTone,
  type WorkItem,
  type WorkItemDetail,
  type WorkItemSessionRef,
} from "./read.ts";

/** Sentinel for "create a new working copy (worktree)" — not a folder name, so it cannot
 * collide with one. */
export const NEW_WORKTREE = " new-worktree";

/** The start controls, as a section or as a fold. `<details>` and not a button of our own: it
 * keeps the open/closed state, the keyboard behaviour and the disclosure triangle the browser
 * already gives, and a folded section still contains real, focusable controls. */
function StartSection({ fold, title, children }: { fold: boolean; title: string; children: ReactNode }) {
  if (!fold) {
    return (
      <section className="wi-dstart">
        <h4>{title}</h4>
        {children}
      </section>
    );
  }
  return (
    <details className="wi-dstart wi-dfold">
      <summary>{title}</summary>
      {children}
    </details>
  );
}

/** Read the pull request again, live, when the panel opens (docs/log/80 §80.24).
 *
 * Only pull requests, and only on a human opening the panel — this spends provider calls, which
 * is exactly why the rail's own refresh stays on a five-minute timer and why nothing here is
 * cached: neither af nor the CP stores the answer.
 *
 * A stopped Workspace answers 409 and is NOT started for a panel (ADR 0061 decision 1). That is
 * the feature working as designed, not a failure, so it is told apart from a real error: the
 * panel keeps the cached row and says which of the two happened. */
function useLiveDetail(item: WorkItem) {
  const [detail, setDetail] = useState<WorkItemDetail | null>(null);
  const [err, setErr] = useState("");
  const [stopped, setStopped] = useState(false);
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const { provider, key, kind } = item;
  useEffect(() => {
    if (!canReadLive({ provider, kind })) return;
    // The panel can be re-pointed at another row while a read is in flight (or closed under it).
    // Without this guard the late answer would paint the previous pull request's numbers onto
    // the one now on screen.
    let current = true;
    setBusy(true);
    setErr("");
    setStopped(false);
    setDetail(null);
    void workItemDetail({ provider, key })
      .then((res) => {
        if (!current) return;
        const got = readWorkItemDetail(res);
        if (got.detail) {
          setDetail(got.detail);
          return;
        }
        const code = got.error && typeof got.error === "object" ? got.error.code : "";
        if (code === "workspace_stopped" || code === "agent_outdated") {
          setStopped(true);
          return;
        }
        setErr(errText(got.error) || " ");
      })
      .finally(() => {
        if (current) setBusy(false);
      });
    return () => {
      current = false;
    };
  }, [provider, key, kind, attempt]);
  return { detail, err, stopped, busy, retry: () => setAttempt((n) => n + 1) };
}

interface Props {
  item: WorkItem;
  repos: Repo[];
  /** Working copy resolved from the query's default or the item's own repo ("" = none). */
  defaultRepo: string;
  /** Ledger rows for this item — shows a second person on the same ticket before they launch. */
  started: WorkItemSessionRef[];
  onClose(): void;
  /** target = the working copy to launch in; inPlace = the user picked an existing copy
   * (so the launch dialog must not re-default to "new worktree"). */
  onPick(target: Repo, inPlace: boolean): void;
  /** Called when there is no working copy at all: defer to the start hub, which has the clone
   * path. */
  onStartHub(): void;
  onOpenSession(name: string): void;
  onReport(): void;
}

export function WorkItemDetailModal({
  item,
  repos,
  defaultRepo,
  started,
  onClose,
  onPick,
  onStartHub,
  onOpenSession,
  onReport,
}: Props) {
  const tr = useT();
  const live = useLiveDetail(item);
  // A pull request's panel is a place to decide whether to pick the review up, so the live read
  // leads and the cached row is the fallback — never the other way round.
  const view = live.detail || item;
  const isPR = item.kind === "pr";
  const bases = useMemo(() => repos.filter((r) => !r.worktree), [repos]);
  // Default repository: the query's hint, then the item's repo, then the first one. A worktree
  // as the default resolves to its parent; where to launch is chosen on the row below.
  const initialBase = useMemo(() => {
    const hit = repos.find((r) => r.name === defaultRepo);
    if (hit) return hit.worktree ? hit.parent || "" : hit.name;
    return bases[0]?.name || "";
  }, [repos, bases, defaultRepo]);
  const [base, setBase] = useState(initialBase);
  const [where, setWhere] = useState<string>(NEW_WORKTREE);

  const baseRepo = repos.find((r) => r.name === base);
  // That base's worktrees in creation order. An SVN working copy has no worktree concept.
  const worktrees = useMemo(
    () => repos.filter((r) => r.worktree && r.parent === base).sort((a, b) => (a.createdAt || "").localeCompare(b.createdAt || "")),
    [repos, base],
  );
  // `git worktree add` cannot resolve HEAD in a working copy with no commit yet (unborn).
  const canWorktree = !!baseRepo && baseRepo.vcs !== "svn" && !baseRepo.unborn;

  const go = () => {
    // With no working copy yet there is nothing to choose here, so hand over to the start hub,
    // which guides from a clone (the caller has already seeded the prompt).
    if (bases.length === 0) {
      onStartHub();
      return;
    }
    if (!baseRepo) return;
    if (where === NEW_WORKTREE && canWorktree) {
      onPick(baseRepo, false);
      return;
    }
    const chosen = repos.find((r) => r.name === where) || baseRepo;
    onPick(chosen, true);
  };

  const rel = relTime(view.updatedAt);
  const d = live.detail;

  // "Can this be merged" in one phrase. Draft comes before the merge check because a draft is
  // not waiting on anyone, and "unknown" is said out loud: GitHub answers null while it is still
  // computing and Bitbucket never answers at all, and calling either of those "no conflicts"
  // would send a reviewer to a branch that does not apply.
  const mergeText = (pr: WorkItemDetail): string =>
    pr.merged
      ? tr("wi.detail_merge_merged")
      : pr.draft
        ? tr("wi.detail_merge_draft")
        : pr.mergeable === "clean"
          ? tr("wi.detail_merge_clean")
          : pr.mergeable === "conflict"
            ? tr("wi.detail_merge_conflict")
            : tr("wi.detail_merge_unknown");

  // The counts stay in the line: "failing" cannot say whether one job of forty is red.
  const checksText = (c: WorkItemDetail["checks"]): string =>
    c.state === "failure"
      ? tr("wi.detail_checks_failed", { failed: c.failed, total: c.total })
      : c.state === "pending"
        ? tr("wi.detail_checks_pending", { pending: c.pending, total: c.total })
        : c.state === "success"
          ? tr("wi.detail_checks_ok", { total: c.total })
          : "";

  const reviewSummary = (pr: WorkItemDetail): string => {
    const c = reviewCounts(pr.reviews);
    return tr("wi.detail_reviews_sum", { ok: c.approved, ng: c.changes, waiting: c.pending });
  };

  return (
    // The heading never shortens the key. The rail row drops the repo and shows just `#312`,
    // but only the full key says which of 41 rows was opened (`#312` can exist in three
    // repositories at once).
    // The heading word is the kind itself (issue / pull request), so that a third name for this
    // thing never reaches the UI — the rail and the settings tab both use one term.
    <Modal
      title={tr("wi.detail_title", { kind: tr(item.kind === "pr" ? "wi.kind_pr" : "wi.kind_issue"), key: item.key })}
      onClose={onClose}
      className="wi-dmodal"
    >
      {/* Content must sit in ui-modal-body / ui-modal-foot. ui-modal itself has no padding (the
          heading and footer carry their own), so a child placed directly in it sticks to the
          frame. */}
      <div className="ui-modal-body">
        <div className="wi-dhead">
          <span className={`wi-dot tone-${stateTone(view.state)}`} title={stateLabel(view.state)}>
            <Icon name={isPR ? "git-pull-request" : "issues"} />
          </span>
          {/* Never ellipsised here: this is the panel people open to read what the rail row cut
              to one line, so it wraps and shows the title in full. */}
          <h3 className="wi-dtitle">{view.title}</h3>
        </div>

        {/* Always say which of the two this is. A panel that silently shows a five-minute-old
            row looks exactly like one showing the live pull request, and the difference is the
            whole point of the live read (§80.24). */}
        {canReadLive(item) && (
          <p className={"wi-dlive" + (live.err ? " bad" : "")} role="status" title={live.err || undefined}>
            <Icon
              name={live.busy ? "sync" : live.err ? "warning" : live.stopped ? "debug-pause" : "check"}
              spin={live.busy}
            />
            <span>
              {live.busy
                ? tr("wi.detail_live_loading")
                : live.err
                  ? tr("wi.detail_live_failed")
                  : live.stopped
                    ? tr("wi.detail_live_stopped")
                    : tr("wi.detail_live_fresh")}
            </span>
            {!live.busy && (live.err || live.stopped) && (
              <button type="button" className="linklike" onClick={live.retry}>
                {tr("wi.detail_live_retry")}
              </button>
            )}
          </p>
        )}

        {/* Exactly the fields the CP holds. A row with no value is not drawn; a column of
            em-dashes only adds things to read. */}
        <dl className="wi-dfacts">
          <dt>{tr("wi.detail_state")}</dt>
          <dd>{stateLabel(view.state)}</dd>
          <dt>{tr("wi.detail_kind")}</dt>
          <dd>{isPR ? tr("wi.kind_pr") : tr("wi.kind_issue")}</dd>
          <dt>{tr("wi.detail_provider")}</dt>
          <dd>
            <span className="wi-dprov">{item.provider}</span>
          </dd>
          {d?.author && (
            <>
              <dt>{tr("wi.detail_author")}</dt>
              <dd>@{d.author}</dd>
            </>
          )}
          {view.assignee && (
            <>
              <dt>{tr("wi.detail_assignee")}</dt>
              <dd>@{view.assignee}</dd>
            </>
          )}
          {view.repo && (
            <>
              <dt>{tr("wi.detail_repo")}</dt>
              <dd>{view.repo}</dd>
            </>
          )}
          {/* From here down: only what the live read brought back. These are the fields that
              decide whether to pick a review up, and every one of them can change between two
              rail refreshes — which is why none of them is cached (§80.24). */}
          {d && (d.baseBranch || d.headBranch) && (
            <>
              <dt>{tr("wi.detail_branches")}</dt>
              <dd className="wi-dbranches">
                <code>{d.baseBranch}</code>
                <Icon name="arrow-left" />
                <code>{d.headBranch}</code>
              </dd>
            </>
          )}
          {d && (
            <>
              <dt>{tr("wi.detail_merge")}</dt>
              <dd className={d.mergeable === "conflict" && !d.merged ? "tone-bad" : undefined}>{mergeText(d)}</dd>
            </>
          )}
          {d && (d.changedFiles > 0 || d.additions > 0 || d.deletions > 0) && (
            <>
              <dt>{tr("wi.detail_diff")}</dt>
              <dd className="wi-ddiff">
                <span className="wi-dadd">+{d.additions}</span>
                <span className="wi-ddel">−{d.deletions}</span>
                <span className="wi-dfiles">{tr("wi.detail_diff_files", { n: d.changedFiles })}</span>
              </dd>
            </>
          )}
          {d && d.reviews.length > 0 && (
            <>
              <dt>{tr("wi.detail_reviews")}</dt>
              <dd className="wi-dreviews" title={reviewSummary(d)}>
                {d.reviews.map((r) => (
                  <span className={`wi-dreview ${r.state}`} key={`${r.state}:${r.name}`}>
                    <Icon name={r.state === "approved" ? "check" : r.state === "changes_requested" ? "request-changes" : "clock"} />
                    {r.name}
                  </span>
                ))}
              </dd>
            </>
          )}
          {d && d.checks.total > 0 && (
            <>
              <dt>{tr("wi.detail_checks")}</dt>
              <dd className={`tone-${checksTone(d.checks)}`}>{checksText(d.checks)}</dd>
            </>
          )}
          {d && d.comments > 0 && (
            <>
              <dt>{tr("wi.detail_comments")}</dt>
              <dd>{d.comments}</dd>
            </>
          )}
          {view.labels.length > 0 && (
            <>
              <dt>{tr("wi.detail_labels")}</dt>
              <dd className="wi-dlabels">
                {view.labels.map((l) => (
                  <span className="wi-label" key={l}>
                    {l}
                  </span>
                ))}
              </dd>
            </>
          )}
          {view.updatedAt && (
            <>
              <dt>{tr("wi.detail_updated")}</dt>
              <dd title={fullLocal(view.updatedAt)}>
                {rel ? tr("wi.detail_updated_rel", { at: fullLocal(view.updatedAt), rel }) : fullLocal(view.updatedAt)}
              </dd>
            </>
          )}
        </dl>

        {/* The body is not shown here, so where to go and read it always is (same reason as
            §80.9). A pull request carries the same link as the footer's main button, so it is
            not repeated here. */}
        {!isPR && (
          <a className="wi-dlink" href={view.url} target="_blank" rel="noreferrer noopener">
            <Icon name="link-external" />
            {tr("wi.open_external")}
          </a>
        )}

        {/* Already started. This is the ledger's main payoff: it stops a second person picking
            up the same ticket before they launch (docs/log/80 §80.8). */}
        {started.length > 0 && (
          <section className="wi-dstarted">
            <h4>{tr("wi.detail_started")}</h4>
            <ul>
              {started.map((s) => (
                <li key={s.id}>
                  <button type="button" className="wi-dsession" onClick={() => onOpenSession(s.sessionName)}>
                    <Icon name="circle-filled" />
                    {s.sessionName}
                    {s.branch ? <span className="wi-dbranch">{s.branch}</span> : null}
                  </button>
                </li>
              ))}
            </ul>
            {/* Reporting back. Pressing this posts nothing: it opens a modal for reading the
                draft, and posting is a separate step inside it (ADR 0061 decision 6). */}
            {canComment(item) && (
              <Button variant="ghost" onClick={onReport}>
                <Icon name="comment" />
                {tr("wi.report_title")}
              </Button>
            )}
          </section>
        )}

        {/* Starting a session is the panel's main act for an issue and a second-order one for a
            pull request (docs/log/80 §80.24). A PR is usually opened to look — is it green, does
            it conflict, who has approved it — and reviewing it locally is the less common half,
            so it folds away rather than disappearing: a review still needs a working copy, and
            the started ledger under it is what stops two people picking up the same review. */}
        <StartSection fold={isPR} title={tr(isPR ? "wi.detail_start_head_pr" : "wi.detail_start_head")}>
          {bases.length === 0 ? (
            <p className="wi-shint">{tr("wi.start_no_repos")}</p>
          ) : (
            <>
              <label className="wi-sfield">
                <span>{tr("wi.start_repo")}</span>
                <select
                  value={base}
                  onChange={(e) => {
                    setBase(e.target.value);
                    setWhere(NEW_WORKTREE); // changing the repository re-opens the where choice
                  }}
                >
                  {bases.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </select>
              </label>
              <label className="wi-sfield">
                <span>{tr("wi.start_where")}</span>
                <select value={where} onChange={(e) => setWhere(e.target.value)}>
                  {canWorktree && <option value={NEW_WORKTREE}>{tr("wi.start_new_worktree")}</option>}
                  <option value={base}>{tr("wi.start_in_base", { repo: base })}</option>
                  {worktrees.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.branch ? tr("wi.start_in_worktree", { branch: r.branch }) : r.name}
                    </option>
                  ))}
                </select>
              </label>
              <p className="wi-shint">
                {where === NEW_WORKTREE ? tr("wi.start_new_worktree_hint") : tr("wi.start_existing_hint")}
              </p>
            </>
          )}
          {/* Folded away, the footer's main button is the external link, so the start button has
              to live next to the choices it acts on. */}
          {isPR && (
            <Button onClick={go} disabled={bases.length > 0 && !baseRepo}>
              {tr("wi.start")}
            </Button>
          )}
        </StartSection>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        {isPR ? (
          // The main act for a pull request is going and reading it where it lives. af holds no
          // diff, no review thread and no comment text, so anything more here would be a worse
          // copy of the provider's own page (§80.1).
          <a className="ui-btn ui-btn-primary wi-dopen" href={view.url} target="_blank" rel="noreferrer noopener">
            <Icon name="link-external" />
            {tr("wi.open_provider", { name: item.provider === "bitbucket" ? "Bitbucket" : "GitHub" })}
          </a>
        ) : (
          <Button onClick={go} disabled={bases.length > 0 && !baseRepo}>
            {tr("wi.start")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
