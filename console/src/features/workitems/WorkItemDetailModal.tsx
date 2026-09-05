// Work item detail (docs/log/80 §80.8 / §80.20).
//
// The single panel a rail row opens. The row lost its "start" button because forty-one identical
// buttons down the right edge make the rail look like a surface where pressing does something
// (§80.20); the row is information again and the controls live here.
//
// It shows only the fields the CP already holds (key/title/state/url/assignee/labels/repo/updated).
// It does NOT show the body: the CP is not allowed to hold ticket bodies (ADR 0061 decision 2),
// and fetching one here would rebuild "wake the Workspace just to look at a list" on the detail
// side. The body is read inside the session by `gh` or the MCP (§80.9).
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
import { useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { Repo } from "../repos/store.ts";
import {
  canComment,
  fullLocal,
  relTime,
  stateLabel,
  stateTone,
  type WorkItem,
  type WorkItemSessionRef,
} from "./read.ts";

/** Sentinel for "create a new working copy (worktree)" — not a folder name, so it cannot
 * collide with one. */
export const NEW_WORKTREE = " new-worktree";

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

  const rel = relTime(item.updatedAt);

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
          <span className={`wi-dot tone-${stateTone(item.state)}`} title={stateLabel(item.state)}>
            <Icon name={item.kind === "pr" ? "git-pull-request" : "issues"} />
          </span>
          {/* Never ellipsised here: this is the panel people open to read what the rail row cut
              to one line, so it wraps and shows the title in full. */}
          <h3 className="wi-dtitle">{item.title}</h3>
        </div>

        {/* Exactly the fields the CP holds. A row with no value is not drawn; a column of
            em-dashes only adds things to read. */}
        <dl className="wi-dfacts">
          <dt>{tr("wi.detail_state")}</dt>
          <dd>{stateLabel(item.state)}</dd>
          <dt>{tr("wi.detail_kind")}</dt>
          <dd>{item.kind === "pr" ? tr("wi.kind_pr") : tr("wi.kind_issue")}</dd>
          <dt>{tr("wi.detail_provider")}</dt>
          <dd>
            <span className="wi-dprov">{item.provider}</span>
          </dd>
          {item.assignee && (
            <>
              <dt>{tr("wi.detail_assignee")}</dt>
              <dd>@{item.assignee}</dd>
            </>
          )}
          {item.repo && (
            <>
              <dt>{tr("wi.detail_repo")}</dt>
              <dd>{item.repo}</dd>
            </>
          )}
          {item.labels.length > 0 && (
            <>
              <dt>{tr("wi.detail_labels")}</dt>
              <dd className="wi-dlabels">
                {item.labels.map((l) => (
                  <span className="wi-label" key={l}>
                    {l}
                  </span>
                ))}
              </dd>
            </>
          )}
          {item.updatedAt && (
            <>
              <dt>{tr("wi.detail_updated")}</dt>
              <dd title={fullLocal(item.updatedAt)}>
                {rel ? tr("wi.detail_updated_rel", { at: fullLocal(item.updatedAt), rel }) : fullLocal(item.updatedAt)}
              </dd>
            </>
          )}
        </dl>

        {/* The body is not shown here, so where to go and read it always is (same reason as
            §80.9). */}
        <a className="wi-dlink" href={item.url} target="_blank" rel="noreferrer noopener">
          <Icon name="link-external" />
          {tr("wi.open_external")}
        </a>

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

        <section className="wi-dstart">
          <h4>{tr("wi.detail_start_head")}</h4>
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
        </section>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        <Button onClick={go} disabled={bases.length > 0 && !baseRepo}>
          {tr("wi.start")}
        </Button>
      </footer>
    </Modal>
  );
}
