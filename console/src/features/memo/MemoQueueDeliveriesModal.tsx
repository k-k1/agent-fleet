// The runs of one auto-run category (ADR 0137 §10): each delivery with its state, its messages
// and a link to the session it started, plus "Run now". Read only for the category the person
// opened, and re-read on a slow poll while the dialog is open, so it adds nothing to the memo
// section's own poll.
import { useCallback, useEffect, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errDetail } from "../../core/api/client.ts";
import { openSessionChat } from "../sessions/open.ts";
import type { MemoDelivery } from "../../types/memo.ts";
import { memoQueueDeliveries, memoQueueFire } from "./api.ts";
import { deliveryStateKey, readDeliveries } from "./queues.ts";

const POLL_MS = 10000;

interface Props {
  categoryId: string;
  categoryName: string;
  /** How many messages are waiting to run now (the fire button needs at least one). */
  waiting: number;
  onClose: () => void;
  /** A fire took messages: the memo section refetches. */
  onFired: () => void;
}

export function MemoQueueDeliveriesModal({ categoryId, categoryName, waiting, onClose, onFired }: Props) {
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
        toast(errDetail(res.error) || t("memo.d_fire_failed"), { kind: "warn" });
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

  return (
    <Modal title={tr("memo.autorun_deliveries_title", { name: categoryName })} onClose={onClose} className="memo-q-modal" lockClose={busy}>
      <div className="ui-modal-body">
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
              return (
                <li key={d.id} className="memo-d-row">
                  <span className={"memo-d-state s-" + (d.state === "closed" ? d.outcome || "closed" : d.state)}>
                    {k ? tr(k) : tr("memo.d_unknown_state", { state: d.state })}
                  </span>
                  <span className="memo-d-n">{tr("memo.d_messages", { count: d.messageIds.length })}</span>
                  <span className="memo-d-at">{d.createdAt}</span>
                  {d.session ? (
                    <button
                      type="button"
                      className="linkish memo-d-session"
                      title={tr("memo.d_open_session", { name: d.session })}
                      onClick={() => {
                        openSessionChat(d.session);
                        onClose();
                      }}
                    >
                      {d.session}
                    </button>
                  ) : (
                    <span className="muted">{tr("memo.d_no_session")}</span>
                  )}
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
