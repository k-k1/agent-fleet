// BranchList — filterable, newest-commit-first branch list. Shared by the repo
// branch-switch modal (local, checkout on pick) and the new-session repo picker
// (remote, select on pick). Port of the old components/BranchList.
import { useMemo, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { relTime as intlRelTime } from "../../lib/intl.ts";

// relTime renders a unix-seconds timestamp as a short locale-aware "… ago" label.
// Converts the seconds to milliseconds and delegates to the shared lib/intl
// implementation; exported because RepoPicker imports it too.
export const relTime = (unix: number | undefined): string => (unix ? intlRelTime(unix * 1000) : "");

// A branch row: null = loading list, [] = none. Field names mirror the Agent's
// /repos/{name}/branches payload verbatim, so the list can be rendered straight
// from the response without a mapping layer that could silently drop a field.
export interface Branch {
  name: string;
  unix?: number;
  date?: string;
  subject?: string;
  default?: boolean;
  remote?: boolean;
  current?: boolean;
  /** Another working copy that already has this branch checked out. git allows a
   * branch in one worktree at a time, so such a row is not a valid target for a
   * checkout OR a new worktree — it renders as its holder instead. */
  worktree_path?: string;
}

/** wtFolder reduces a working-copy path to its folder name — the id the user sees
 * everywhere else in the Console (~/repos/<folder>). */
export const wtFolder = (p: string | undefined): string => (p || "").split("/").filter(Boolean).pop() || "";

interface BranchListProps {
  branches: Branch[] | null;
  selected?: string;
  onPick: (name: string) => void;
  busy?: string | boolean;
  disableActive?: boolean;
  autoFocus?: boolean;
  /** Where an occupied row leads. Given a handler, the row stays clickable and opens
   * the working copy that holds the branch; without one it is simply disabled. Either
   * way it never falls through to onPick — git would refuse that anyway, and failing
   * after the click is exactly the dead end this list exists to prevent. */
  onOpenWorktree?: (folder: string, branch: Branch) => void;
  /** Adds a per-row shortcut that starts work on that branch in its OWN working copy
   * instead of moving this one's HEAD. Offered where switching in place is the risky
   * choice (a worktree, which is meant to be one task on one branch). */
  onStartWork?: (name: string) => void;
}

export function BranchList({ branches, selected, onPick, busy, disableActive, autoFocus, onOpenWorktree, onStartWork }: BranchListProps) {
  const tr = useT();
  const [filter, setFilter] = useState("");

  const shown = useMemo(() => {
    const list = [...(branches || [])].sort((a, b) => (b.unix || 0) - (a.unix || 0));
    const q = filter.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (b) => b.name.toLowerCase().includes(q) || (b.subject || "").toLowerCase().includes(q),
    );
  }, [branches, filter]);

  return (
    <div className="branchlist-wrap">
      <input
        className="branch-filter"
        autoFocus={autoFocus}
        placeholder={tr("rp.filter_branches")}
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      {branches === null ? (
        <p className="pick-muted">{tr("rp.loading")}</p>
      ) : shown.length === 0 ? (
        <p className="pick-muted">{tr("rp.no_branches")}</p>
      ) : (
        <ul className="branch-list">
          {shown.map((b) => {
            const active = b.name === selected || b.current;
            const holder = wtFolder(b.worktree_path);
            return (
              <li key={(b.remote ? "r:" : "l:") + b.name} className="branch-row">
                <button
                  type="button"
                  className={"branch-item" + (active ? " current" : "") + (holder ? " occupied" : "")}
                  disabled={!!busy || (active && disableActive) || (!!holder && !onOpenWorktree)}
                  onClick={() => (holder ? onOpenWorktree?.(holder, b) : onPick(b.name))}
                  title={holder ? tr("rp.branch_in_use_title", { folder: holder }) : b.date || ""}
                >
                  <span className="bi-main">
                    <span className="bi-name">
                      {active ? "● " : ""}
                      {b.name}
                    </span>
                    {b.default && <span className="bi-remote">default</span>}
                    {b.remote && <span className="bi-remote">remote</span>}
                    {holder && (
                      <span className="bi-remote bi-wt">
                        {tr("rp.branch_in_use_chip", { folder: holder })}
                      </span>
                    )}
                    <span className="bi-time">{relTime(b.unix)}</span>
                  </span>
                  {b.subject && <span className="bi-subject">{b.subject}</span>}
                </button>
                {onStartWork && !holder && !b.current && (
                  <button
                    type="button"
                    className="branch-side"
                    disabled={!!busy}
                    title={tr("rp.branch_start_work", { name: b.name })}
                    aria-label={tr("rp.branch_start_work", { name: b.name })}
                    onClick={() => onStartWork(b.name)}
                  >
                    <Icon name="play" />
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
