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
import type { MemoDelivery } from "../../types/memo.ts";
import { memoQueueDeliveries } from "./api.ts";
import { deliveryStateKey, firstTurnWords, heldWords, readDeliveries } from "./queues.ts";

const POLL_MS = 30000;
const SHOWN = 5;

export function MemoQueueRuns({ categoryId, onShowAll }: { categoryId: string; onShowAll: () => void }) {
  const tr = useT();
  const [rows, setRows] = useState<MemoDelivery[] | null>(null);
  const [failed, setFailed] = useState(false);
  const alive = useRef(true);

  const load = useCallback(async () => {
    try {
      const got = readDeliveries(await memoQueueDeliveries(categoryId));
      if (!alive.current) return;
      if (got) {
        setRows(got);
        setFailed(false);
      } else setFailed(true);
    } catch {
      if (alive.current) setFailed(true);
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
        const turn = firstTurnWords(d);
        const openable = !!d.session;
        const open = () => openSessionChat(d.session);
        const tip = [openable ? tr("memo.d_open_session", { name: d.session }) : "", turn ? tr(turn) : ""].filter(Boolean).join(" · ");
        return (
          <div
            key={d.id}
            className={"memo-run" + (openable ? " is-openable" : "")}
            role={openable ? "button" : undefined}
            tabIndex={openable ? 0 : undefined}
            title={tip || undefined}
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
            {why && <span className="memo-run-held">{tr(why)}</span>}
            {openable && <Icon name="link-external" className="memo-run-open" />}
          </div>
        );
      })}
      {rows.length > SHOWN && (
        <button type="button" className="linkish sm memo-runs-all" onClick={onShowAll}>
          {tr("memo.d_runs_all", { count: rows.length })}
        </button>
      )}
    </div>
  );
}
