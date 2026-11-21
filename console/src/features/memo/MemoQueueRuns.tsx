// The latest runs of one auto-run category, inline under its header (the left-pane twin of the
// schedule run list). Read-only: "Run now", the tidy history and the full list stay in the
// deliveries dialog, reached through "show all". It is mounted only while the category's list is
// expanded, so a collapsed category reads nothing and the memo section's own poll stays free of
// deliveries.
import { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { openSessionChat } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useAutoRunReveal } from "../autorun/store.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { errText } from "../../core/api/client.ts";
import type { MemoDelivery } from "../../types/memo.ts";
import { memoQueueDeliveries, memoQueueDeliveryEnd } from "./api.ts";
import { deferWords, deliveryStateKey, firstTurnWords, heldNames, heldWords, readDeliveries } from "./queues.ts";

const POLL_MS = 30000;
const SHOWN = 5;

export function MemoQueueRuns({ categoryId, repo, onShowAll }: { categoryId: string; /** The category's repository, "" for none: the section knows it before the shared queue list does. */ repo: string; onShowAll: () => void }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [ending, setEnding] = useState<string | null>(null);
  const [rows, setRows] = useState<MemoDelivery[] | null>(null);
  const [failed, setFailed] = useState(false);
  const alive = useRef(true);
  // A run whose session the Auto-run view lists can be shown there.
  const runNames = useSessionsStore((s) => s.sessions);
  const listed = new Set(runNames.filter((x) => x.origin === "queue").map((x) => x.name));
  // The Auto-run view lists only the categories of the active working set; a link to a run it
  // hides would land on nothing.
  const wset = useActiveWorkingSet();
  const hidden = !!wset && !!repo && !wset.repos.includes(repo);
  // Only the newest read may land: a slow earlier one must not overwrite a later answer.
  const seq = useRef(0);

  const load = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const got = readDeliveries(await memoQueueDeliveries(categoryId));
      if (!alive.current || mine !== seq.current) return;
      if (got) {
        setRows(got);
        setFailed(false);
      } else setFailed(true);
    } catch {
      if (alive.current && mine === seq.current) setFailed(true);
    }
  }, [categoryId]);

  useEffect(() => {
    alive.current = true;
    void load();
    const id = setInterval(() => {
      if (!document.hidden) void load();
    }, POLL_MS);
    return () => {
      alive.current = false;
      clearInterval(id);
    };
  }, [load]);

  // Ends an open run after a confirmation (it stops the run's session). The server is idempotent,
  // so a second press or a run the tick already closed answers the same; the list is re-read after.
  const end = async (d: MemoDelivery) => {
    if (ending) return;
    const ok = await askConfirm({
      title: tr("memo.d_end_title"),
      body: tr("memo.d_end_body", { name: d.session }),
      confirmLabel: tr("memo.d_end_confirm"),
      danger: true,
    });
    if (!ok) return;
    setEnding(d.id);
    try {
      const res = await memoQueueDeliveryEnd(categoryId, d.id);
      if (res?.error) toast(errText(res.error) || tr("memo.d_end_failed"), { kind: "warn" });
      else toast(tr("memo.d_end_done"), { kind: "success" });
    } catch {
      toast(tr("memo.d_end_failed"), { kind: "warn" });
    } finally {
      if (alive.current) setEnding(null);
      void load();
    }
  };

  if (rows === null) {
    return (
      <div className="memo-runs">
        <div className={failed ? "memo-runs-err" : "memo-runs-note"} role={failed ? "alert" : undefined}>
          {failed ? tr("memo.d_load_failed") : tr("common.loading")}
        </div>
      </div>
    );
  }
  const shown = rows.slice(0, SHOWN);
  return (
    <div className="memo-runs">
      {failed && (
        <div className="memo-runs-err" role="alert">
          {tr("memo.d_load_failed")}
        </div>
      )}
      {rows.length === 0 && !failed && <div className="memo-runs-note">{tr("memo.d_empty")}</div>}
      {shown.map((d) => {
        const k = deliveryStateKey(d);
        const why = heldWords(d);
        const heldText = why ? (heldNames(d) ? tr("memo.d_held_wakers_named", { names: heldNames(d) }) : tr(why)) : "";
        const turn = firstTurnWords(d);
        const openable = !!d.session;
        const open = () => openSessionChat(d.session);
        const deferred = deferWords(d);
        const tip = [
          openable ? tr("memo.d_open_session", { name: d.session }) : "",
          turn ? tr(turn) : "",
          deferred && d.defer ? tr(deferred) + ": " + d.defer.reason : "",
        ]
          .filter(Boolean)
          .join(" · ");
        return (
          <div
            key={d.id}
            className={"memo-run" + (openable ? " is-openable" : "")}
            role={openable ? "button" : undefined}
            tabIndex={openable ? 0 : undefined}
            title={tip || undefined}
            aria-label={openable ? [k ? tr(k) : d.state, fmtDateTime(d.createdAt), tr("memo.d_open_session", { name: d.session })].join(", ") : undefined}
            onClick={openable ? open : undefined}
            onKeyDown={
              openable
                ? (e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      open();
                    }
                  }
                : undefined
            }
          >
            <span className={"memo-d-state s-" + (d.state === "closed" ? d.outcome || "closed" : d.state)}>
              {k ? tr(k) : tr("memo.d_unknown_state", { state: d.state })}
            </span>
            <span className="memo-run-time">{fmtDateTime(d.createdAt)}</span>
            <span className="memo-d-tag">{tr(d.auto ? "memo.d_auto_short" : "memo.d_manual_short")}</span>
            {why && <span className="memo-run-held" title={heldText}>{heldText}</span>}
            {d.state === "open" && openable && (
              <button
                type="button"
                className="linkish memo-run-end"
                disabled={ending === d.id}
                onClick={(e) => {
                  e.stopPropagation();
                  void end(d);
                }}
                onKeyDown={(e) => e.stopPropagation()}
              >
                {tr("memo.d_end")}
              </button>
            )}
            {listed.has(d.session) && (
              <button
                type="button"
                className="linkish memo-run-view"
                title={hidden ? tr("memo.d_show_run_hidden") : tr("memo.d_show_run")}
                disabled={hidden}
                onClick={(e) => {
                  e.stopPropagation();
                  useAutoRunReveal.getState().reveal(d.session);
                }}
                onKeyDown={(e) => e.stopPropagation()}
              >
                <Icon name="play" /> {tr("memo.d_show_run")}
              </button>
            )}
            {openable && <Icon name="link-external" className="memo-run-open" />}
          </div>
        );
      })}
      {rows.length > SHOWN && (
        <button type="button" className="linkish memo-runs-all" onClick={onShowAll}>
          {tr("memo.d_runs_all", { count: rows.length })}
        </button>
      )}
    </div>
  );
}
