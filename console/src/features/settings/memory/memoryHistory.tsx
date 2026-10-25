// MemoryHistory — "History of this memory" in the Memories detail pane: the change entries of one
// memory, picked out of the changes list the Changes view reads. The Agent has no per-memory
// history route, so the changes of every memory are asked for page by page and filtered here;
// while older pages remain the list may be missing entries of this memory, and the pane says so
// and offers to load the next page.
// An entry's diff is the Changes view's own ChangeDiffBody.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { ChangeDiffBody, opLabel } from "./memoryChanges.tsx";
import type { MemoryChange } from "./memoryTypes.ts";

/** The most the Agent answers in one page of changes. */
export const HISTORY_LIMIT = 300;

export interface HistoryTarget {
  scope: "user" | "project";
  /** Project id; "" for the user scope. */
  project: string;
  name: string;
}

/** The newest HISTORY_LIMIT changes of every memory, read once by the Memories view and shared by the
 *  history of whichever memory is picked: picking another memory only filters this array. Read again
 *  when `version` moves (a write of this view, or the parent's reload). Older pages are read on
 *  demand (`loadOlder`), by the commit cursor the Agent gives, so the history is reachable all the
 *  way back. */
export interface AllChanges {
  /** null: not read yet. */
  changes: MemoryChange[] | null;
  /** Older changes exist that are not loaded yet. */
  capped: boolean;
  err: string;
  loadOlder?: () => void;
  loadingOlder?: boolean;
  olderErr?: boolean;
}

export function useAllChanges(version: number): AllChanges {
  const [state, setState] = useState<AllChanges>({ changes: null, capped: false, err: "" });
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [olderErr, setOlderErr] = useState(false);
  // An answer for an older read (a reload moved on meanwhile) is dropped.
  const gen = useRef(0);
  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api(`api/agents/memory/entries/changes?limit=${HISTORY_LIMIT}`);
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    gen.current++;
    setBusy(false);
    setOlderErr(false);
    const all: MemoryChange[] = res?.error ? [] : (res?.changes ?? []);
    setNext(res?.error ? "" : (res?.next ?? ""));
    setState({
      changes: all,
      capped: !res?.error && !!res?.next,
      err: res?.error ? errText(res.error) : "",
    });
    return true;
  }, []);
  useRetryLoad(load, [version]);
  const loadOlder = useCallback(async () => {
    const g = gen.current;
    setBusy(true);
    setOlderErr(false);
    const res = await api(`api/agents/memory/entries/changes?limit=${HISTORY_LIMIT}&before=${encodeURIComponent(next)}`).catch(
      () => ({ error: true }),
    );
    if (g !== gen.current) return;
    setBusy(false);
    if (res?.error) return setOlderErr(true);
    const older: MemoryChange[] = res?.changes ?? [];
    setNext(res?.next ?? "");
    setState((prev) => {
      const have = new Set((prev.changes ?? []).map((c) => c.commit));
      return {
        ...prev,
        changes: [...(prev.changes ?? []), ...older.filter((c) => !have.has(c.commit))],
        capped: !!res?.next,
      };
    });
  }, [next]);
  return { ...state, loadOlder, loadingOlder: busy, olderErr };
}

export function MemoryHistory({
  target,
  data,
  onOpenChanges,
}: {
  target: HistoryTarget;
  data: AllChanges;
  onOpenChanges?: () => void;
}) {
  const tr = useT();
  const [sel, setSel] = useState("");
  const { scope, project, name } = target;
  const { capped, err, loadOlder, loadingOlder, olderErr } = data;
  const entries = useMemo(
    () =>
      data.changes?.filter(
        (c) => c.name === name && c.scope === scope && (scope === "user" || c.project?.id === project),
      ) ?? null,
    [data.changes, scope, project, name],
  );
  // An entry that is no longer in the history (a purge rewrote it) must not keep its diff on screen.
  useEffect(() => {
    if (entries && sel && !entries.some((c) => c.commit === sel)) setSel("");
  }, [entries, sel]);

  const who = (c: MemoryChange) =>
    c.authorKind === "member" ? tr("mem.af_by_member") : `${c.authorKind} · ${c.authorSession}`;

  return (
    <section className="mem-md-history" aria-labelledby="mem-md-history-h">
      <h5 id="mem-md-history-h">{tr("mem.md_hist_title")}</h5>
      {err && <p className="mem-warn">{err}</p>}
      {capped && <p className="muted ds-hint">{tr("mem.md_hist_capped", { n: data.changes?.length ?? 0 })}</p>}
      {olderErr && <p className="mem-warn">{tr("mem.md_hist_more_failed")}</p>}
      <ul className="mem-list mem-md-hist-list">
        {entries === null ? (
          <li className="muted pad">{tr("common.loading")}</li>
        ) : entries.length === 0 ? (
          !err && <li className="muted pad">{tr("mem.md_hist_empty")}</li>
        ) : (
          entries.map((c) => (
            <li key={c.commit}>
              <button
                type="button"
                className={"mem-snap" + (c.commit === sel ? " active" : "")}
                aria-pressed={c.commit === sel}
                onClick={() => setSel(c.commit === sel ? "" : c.commit)}
              >
                <span className="mem-snap-when">{fmtDateTime(c.at, DATETIME_FULL)}</span>
                <span className={"mem-badge af-op-" + c.op}>{opLabel(c.op)}</span>
                <span className="mem-snap-what muted">{who(c)}</span>
              </button>
            </li>
          ))
        )}
      </ul>
      {sel && (
        <div className="mem-diff">
          <ChangeDiffBody commit={sel} />
        </div>
      )}
      {capped && loadOlder && (
        <button type="button" className="mem-md-link" disabled={loadingOlder} onClick={loadOlder}>
          {tr("mem.md_hist_more")}
        </button>
      )}
      {onOpenChanges && (
        <button type="button" className="mem-md-link" onClick={onOpenChanges}>
          {tr("mem.md_hist_all")}
        </button>
      )}
    </section>
  );
}
