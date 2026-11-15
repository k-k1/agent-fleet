// The runs of one auto-run category (ADR 0137 §10): each delivery with its state, its messages
// and a link to the session it started, plus "Run now". A run that is still open says why (working,
// waiting for you, ...), whether it is overdue, whether it started by itself, how its first turn
// ended and when a person took its session over. A paused category says so on top, with the button
// that resumes it. Read only for the category the person
// opened, and re-read on a slow poll while the dialog is open, so it adds nothing to the memo
// section's own poll.
import { useCallback, useEffect, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { openSessionChat } from "../sessions/open.ts";
import type { MemoDelivery, MemoQueue } from "../../types/memo.ts";
import { memoQueueDeliveries, memoQueueFire } from "./api.ts";
import { MemoQueueNotices } from "./MemoQueueNotices.tsx";
import { deliveryStateKey, firstTurnWords, heldWords, readDeliveries } from "./queues.ts";

const POLL_MS = 10000;

interface Props {
  categoryId: string;
  categoryName: string;
  /** The category's live settings (undefined: it has none). */
  queue: MemoQueue | undefined;
  /** How many messages are waiting to run now (the fire button needs at least one). */
  waiting: number;
  onClose: () => void;
  /** A fire took messages: the memo section refetches. */
  onFired: () => void;
  /** Lifts the pause; resolves true when it was lifted. The caller refetches. */
  onResume: () => Promise<boolean>;
}

export function MemoQueueDeliveriesModal({ categoryId, categoryName, queue, waiting, onClose, onFired, onResume }: Props) {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<MemoDelivery[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
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

  const fire = async () => {
    if (busy || waiting === 0) return;
    setBusy(true);
    try {
      const res = await memoQueueFire(categoryId);
      if (res?.error) {
        toast(errText(res.error) || t("memo.d_fire_failed"), { kind: "warn" });
        return;
      }
      toast(t("memo.d_fire_started"), { kind: "success" });
      onFired();
      void load();
    } catch {
      toast(t("memo.d_fire_failed"), { kind: "warn" });
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const resume = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await onResume();
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  return (
    <Modal title={tr("memo.autorun_deliveries_title", { name: categoryName })} onClose={onClose} className="memo-q-modal" lockClose={busy}>
      <div className="ui-modal-body">
        {queue && <MemoQueueNotices queue={queue} busy={busy} onResume={() => void resume()} />}
        <div className="memo-d-bar">
          <span className="memo-d-waiting">{tr("memo.d_waiting", { count: waiting })}</span>
          <Button small variant="ghost" onClick={() => void load()}>
            {tr("memo.d_refresh")}
          </Button>
          <Button small variant="primary" icon="play" title={tr("memo.d_fire_title")} disabled={busy || waiting === 0} onClick={() => void fire()}>
            {tr("memo.d_fire")}
          </Button>
        </div>
        {rows === null ? (
          <p className={failed ? "ui-field-hint" : "muted"} role={failed ? "alert" : undefined}>
            {failed ? tr("memo.d_load_failed") : tr("common.loading")}
          </p>
        ) : rows.length === 0 ? (
          <p className="muted">{failed ? tr("memo.d_load_failed") : tr("memo.d_empty")}</p>
        ) : (
          <ul className="memo-d-list">
            {rows.map((d) => {
              const k = deliveryStateKey(d);
              const why = heldWords(d);
              const turn = firstTurnWords(d);
              const at = (iso: string) => fmtDateTime(iso);
              const openSession = (name: string) => {
                openSessionChat(name);
                onClose();
              };
              return (
                <li key={d.id} className="memo-d-row">
                  <span className={"memo-d-state s-" + (d.state === "closed" ? d.outcome || "closed" : d.state)}>
                    {k ? tr(k) : tr("memo.d_unknown_state", { state: d.state })}
                  </span>
                  <span className="memo-d-n">{tr("memo.d_messages", { count: d.messageIds.length })}</span>
                  <span className="memo-d-at">{at(d.createdAt)}</span>
                  {d.session ? (
                    <button
                      type="button"
                      className="linkish memo-d-session"
                      title={tr("memo.d_open_session", { name: d.session })}
                      onClick={() => openSession(d.session)}
                    >
                      {d.session}
                    </button>
                  ) : (
                    <span className="muted">{tr("memo.d_no_session")}</span>
                  )}
                  {d.overdue && (
                    <span
                      className="memo-d-tag overdue"
                      title={
                        tr("memo.d_overdue_title", { hours: queue?.maxTaskHours ?? 0 }) +
                        (d.overdueAt ? " " + tr("memo.d_overdue_since", { at: at(d.overdueAt) }) : "")
                      }
                    >
                      {tr("memo.d_overdue")}
                    </span>
                  )}
                  <span className="memo-d-tag">{tr(d.auto ? "memo.d_auto" : "memo.d_manual")}</span>
                  {why && (
                    <span className="memo-d-held" title={d.heldSeenAt ? tr("memo.d_held_seen", { at: at(d.heldSeenAt) }) : undefined}>
                      {tr("memo.d_held", { why: tr(why) })}
                    </span>
                  )}
                  {why && d.heldSession && d.heldSession !== d.session && (
                    <button
                      type="button"
                      className="linkish memo-d-held-session"
                      title={tr("memo.d_open_session", { name: d.heldSession })}
                      onClick={() => openSession(d.heldSession)}
                    >
                      {tr("memo.d_held_in", { name: d.heldSession })}
                    </button>
                  )}
                  {turn && <span className={"memo-d-turn" + (d.firstTurn?.outcome === "clean" ? "" : " bad")}>{tr(turn)}</span>}
                  {d.retire === "released" && <span className="memo-d-tag">{tr("memo.d_taken_over")}</span>}
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} type="button">
          {tr("common.close")}
        </Button>
      </footer>
    </Modal>
  );
}
