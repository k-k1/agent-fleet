// BranchList — filterable, newest-commit-first branch list. Shared by the repo
// branch-switch modal (local, checkout on pick) and the new-session repo picker
// (remote, select on pick). Port of the old components/BranchList.
import { useMemo, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { relTime as intlRelTime } from "../../lib/intl.ts";

// relTime renders a unix-seconds timestamp as a short locale-aware "… ago" label.
// unix は秒なのでミリ秒へ直し、共通実装（lib/intl）へ委譲する（RepoPicker が import）。
export const relTime = (unix: number | undefined): string => (unix ? intlRelTime(unix * 1000) : "");

// A branch row: null = loading list, [] = none.
export interface Branch {
  name: string;
  unix?: number;
  date?: string;
  subject?: string;
  default?: boolean;
  remote?: boolean;
  current?: boolean;
}

interface BranchListProps {
  branches: Branch[] | null;
  selected?: string;
  onPick: (name: string) => void;
  busy?: string | boolean;
  disableActive?: boolean;
  autoFocus?: boolean;
}

export function BranchList({ branches, selected, onPick, busy, disableActive, autoFocus }: BranchListProps) {
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
            return (
              <li key={(b.remote ? "r:" : "l:") + b.name}>
                <button
                  type="button"
                  className={"branch-item" + (active ? " current" : "")}
                  disabled={!!busy || (active && disableActive)}
                  onClick={() => onPick(b.name)}
                  title={b.date || ""}
                >
                  <span className="bi-main">
                    <span className="bi-name">
                      {active ? "● " : ""}
                      {b.name}
                    </span>
                    {b.default && <span className="bi-remote">default</span>}
                    {b.remote && <span className="bi-remote">remote</span>}
                    <span className="bi-time">{relTime(b.unix)}</span>
                  </span>
                  {b.subject && <span className="bi-subject">{b.subject}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
