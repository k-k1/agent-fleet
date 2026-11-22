// The runs of one auto-run category (ADR 0137 §10): each delivery with its state, its messages
// and a link to the session it started. A run that is still open says why (working,
// waiting for you, ...), whether it is overdue, whether it started by itself, how its first turn
// ended and when a person took its session over. A paused category says so on top, with the button
// that resumes it. Below the runs, the tidies that ran before them (what each did, how it ended, and
// a button that undoes it while that is still possible). Read only for the category the person
// opened, and re-read on a slow poll while the dialog is open, so it adds nothing to the memo
// section's own poll.
import { useCallback, useEffect, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { confirmReturnRun } from "./runReturn.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { useInterval } from "../../lib/useInterval.ts";
import { openSessionChat } from "../sessions/open.ts";
import type { Memo, MemoDelivery, MemoQueue, MemoTidyBlocked, MemoTidyRunsWire } from "../../types/memo.ts";
import { memoQueueDeliveries, memoTidyRuns, memoTidyUndo } from "./api.ts";
import { MemoRecipeBadge } from "./MemoRecipe.tsx";
import { MemoQueueNotices } from "./MemoQueueNotices.tsx";
import {
  canReturnRun,
  deferPending,
  deferWords,
  deliveryStateKey,
  firstTurnWords,
  hasRecipe,
  recipeKey,
  heldNames,
  heldWords,
  readDeliveries,
  readTidyRuns,
  tidyBlockedGroups,
  tidyCountMarks,
  tidyReasonKey,
  tidyStateKey,
  undoRefusal,
} from "./queues.ts";

const POLL_MS = 10000;

interface Props {
  categoryId: string;
  categoryName: string;
  /** The category's live settings (undefined: it has none). */
  queue: MemoQueue | undefined;
  /** How many messages are waiting to run now. */
  waiting: number;
  /** The category's messages as the list holds them: a run shows the recipe of its own messages
   *  that do not follow the category's setting. A run whose messages left the list shows none. */
  messages?: Memo[];
  onClose: () => void;
  /** Messages went back to the queue (a run was returned): the memo section refetches. */
  onFired: () => void;
  /** Opened because a "Run now" was refused for want of a slot: the runs holding them are named
   *  from the list below (a toast alone is gone before it is read). */
  slotsFull?: boolean;
  /** A tidy was undone: messages came back and others went, so the memo section refetches. */
  onUndone: () => void;
  /** The tidy run to stand out, when the history was opened from a replaced note. */
  focusRun?: string;
  /** Lifts the pause; resolves true when it was lifted. The caller refetches. */
  onResume: () => Promise<boolean>;
}

export function MemoQueueDeliveriesModal({ categoryId, categoryName, queue, waiting, messages, onClose, onFired, onUndone, focusRun, onResume, slotsFull = false }: Props) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  // The distinct own recipes among a run's messages (one badge each).
  const recipesOf = (ids: string[]): Memo[] => {
    const seen = new Set<string>();
    const out: Memo[] = [];
    for (const m of messages ?? []) {
      if (!ids.includes(m.id) || !hasRecipe(m) || seen.has(recipeKey(m))) continue;
      seen.add(recipeKey(m));
      out.push(m);
    }
    return out;
  };
  const sessions = useSessionsStore((s) => s.sessions);
  // The run whose notes are being returned to the queue.
  const [returning, setReturning] = useState("");
  const [rows, setRows] = useState<MemoDelivery[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [tidy, setTidy] = useState<MemoTidyRunsWire | null>(null);
  const [tidyFailed, setTidyFailed] = useState(false);
  // The run being undone, and what the Control Plane named when it refused one.
  const [undoing, setUndoing] = useState("");
  const [refused, setRefused] = useState<Record<string, MemoTidyBlocked[]>>({});
  const alive = useRef(true);
  // Numbers each read of the runs when it starts. An answer older than the newest one already applied
  // is ignored, so a slow older read cannot overwrite a newer one; judging by the newest read
  // STARTED instead would drop every answer slower than the poll interval. `answered` is the
  // newest number applied (an answer or a failure), and `wanted` the number a slot refusal needs
  // before its notice may claim anything: the notice must not rest on a list read before the refusal.
  const seq = useRef(0);
  const applied = useRef(0);
  const [answered, setAnswered] = useState(0);
  const [wanted, setWanted] = useState(0);
  const slotsAtOpen = useRef(slotsFull);

  const holders = (rows ?? []).filter((d) => d.state === "claimed" || d.state === "sending" || d.state === "open");

  const load = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const got = readDeliveries(await memoQueueDeliveries(categoryId));
      if (!alive.current || mine < applied.current) return;
      applied.current = mine;
      if (got) {
        setRows(got);
        setFailed(false);
      } else setFailed(true);
      setAnswered(mine);
    } catch {
      if (alive.current && mine > applied.current) {
        applied.current = mine;
        setFailed(true);
        setAnswered(mine);
      }
    }
    // The tidy history is read on its own, so a Control Plane without it leaves the runs above alone.
    try {
      const got = readTidyRuns(await memoTidyRuns(categoryId));
      if (!alive.current) return;
      if (got) {
        setTidy(got);
        setTidyFailed(false);
      } else setTidyFailed(true);
    } catch {
      if (alive.current) setTidyFailed(true);
    }
  }, [categoryId]);

  const undo = async (runId: string) => {
    if (undoing) return;
    setUndoing(runId);
    try {
      const res = await memoTidyUndo(runId);
      const blocked = undoRefusal(res);
      if (blocked) {
        setRefused((r) => ({ ...r, [runId]: blocked }));
      } else if (res?.error) {
        toast(errText(res.error) || t("memo.tidy_undo_failed"), { kind: "warn" });
      } else {
        setRefused((r) => {
          const rest = { ...r };
          delete rest[runId];
          return rest;
        });
        toast(t("memo.tidy_undone"), { kind: "success" });
        onUndone();
      }
      // The list says what can still be undone, after either answer.
      void load();
    } catch {
      toast(t("memo.tidy_undo_failed"), { kind: "warn" });
    } finally {
      if (alive.current) setUndoing("");
    }
  };

  // A refusal for want of a slot arrives while the history is open: read the holders as of now, not
  // as of the last poll (a manual open may predate the run that took the slot).
  useEffect(() => {
    if (!slotsFull) return;
    setWanted(seq.current + 1);
    void load();
  }, [slotsFull, load]);

  useEffect(() => {
    alive.current = true;
    // Opened by a slot refusal, the effect above already reads.
    if (!slotsAtOpen.current) void load();
    return () => {
      alive.current = false;
    };
  }, [load]);
  useInterval(() => {
    if (!document.hidden) void load();
  }, POLL_MS);

  const returnRun = async (d: MemoDelivery) => {
    if (returning) return;
    setReturning(d.id);
    try {
      const done = await confirmReturnRun({ categoryId, d, session: sessions.find((x) => x.name === d.session), askConfirm, toast, tr });
      if (done) onFired();
    } finally {
      if (alive.current) setReturning("");
      void load();
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
        </div>
        {slotsFull && (
          <div className="ui-field-hint memo-d-slots-full">
            {answered < wanted || wanted === 0 ? (
              <p role="alert">{tr("common.loading")}</p>
            ) : failed || rows === null ? (
              <p role="alert">{tr("memo.d_load_failed")}</p>
            ) : holders.length === 0 ? (
              <p role="alert">{tr("memo.d_slots_cleared")}</p>
            ) : (
              <>
                <p role="alert">{tr("memo.d_slots_full")}</p>
                <ul className="memo-d-list">
                  {holders.map((d) => {
                    const why = heldWords(d);
                    const names = heldNames(d);
                    // Only an open run has a reported hold; before that the state itself is the answer.
                    const stateWord = !why && d.state !== "open" ? deliveryStateKey(d) : null;
                    return (
                      <li key={d.id}>
                        {tr("memo.d_slots_holder", {
                          name: d.session || tr("memo.d_no_session"),
                          why: names ? tr("memo.d_held_wakers_named", { names }) : tr(why ?? stateWord ?? "memo.d_slots_no_reason"),
                        })}
                      </li>
                    );
                  })}
                </ul>
              </>
            )}
          </div>
        )}
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
              const names = heldNames(d);
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
                  {recipesOf(d.messageIds).map((m) => (
                    <MemoRecipeBadge key={m.id} memo={m} className="memo-d-tag memo-d-recipe" />
                  ))}
                  {why && (
                    <span className="memo-d-held" title={d.heldSeenAt ? tr("memo.d_held_seen", { at: at(d.heldSeenAt) }) : undefined}>
                      {tr("memo.d_held", { why: names ? tr("memo.d_held_wakers_named", { names }) : tr(why) })}
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
                  {d.defer && (
                    <span className={"memo-d-defer" + (!deferPending(d) && d.defer.result !== "applied" ? " dropped" : "")}>
                      {tr(deferWords(d)!)}
                      {d.defer.result === "applied" || deferPending(d)
                        ? " " + tr("memo.d_defer_until", { at: at(d.defer.notBefore) })
                        : ""}
                      <span className="memo-deferred-reason">{d.defer.reason}</span>
                    </span>
                  )}
                  {d.returnBlocked.length > 0 && (
                    <span className="memo-d-held">{tr("memo.d_return_blocked", { ids: d.returnBlocked.map((b) => b.by).join(", ") })}</span>
                  )}
                  {d.returnedAt && <span className="memo-d-tag">{tr("memo.d_returned", { at: at(d.returnedAt) })}</span>}
                  {canReturnRun(d) && (
                    <Button small variant="ghost" title={tr("memo.d_return_hint")} disabled={returning !== ""} onClick={() => void returnRun(d)}>
                      {tr("memo.d_return")}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {(queue?.tidy || (tidy && tidy.runs.length > 0)) && (
          <section className="memo-tidy" aria-label={tr("memo.tidy_title")}>
            <h3 className="memo-tidy-title">{tr("memo.tidy_title")}</h3>
            {tidy && <div className="memo-d-waiting">{tr("memo.tidy_used", { used: tidy.usedToday, cap: tidy.dailyCap })}</div>}
            {queue && !queue.tidy && <div className="ui-field-hint">{tr("memo.tidy_off")}</div>}
            {tidy === null ? (
              <p className={tidyFailed ? "ui-field-hint" : "muted"} role={tidyFailed ? "alert" : undefined}>
                {tidyFailed ? tr("memo.tidy_load_failed") : tr("common.loading")}
              </p>
            ) : tidy.runs.length === 0 ? (
              <p className="muted">{tidyFailed ? tr("memo.tidy_load_failed") : tr("memo.tidy_empty")}</p>
            ) : (
              <ul className="memo-d-list">
                {tidy.runs.map((r) => {
                  const state = tidyStateKey(r.state);
                  const why = tidyReasonKey(r);
                  const counts = r.state === "applied" || r.state === "undone" ? tidyCountMarks(r) : [];
                  const shown = refused[r.id] ?? (r.undoable ? [] : r.blocked);
                  const groups = tidyBlockedGroups(shown);
                  return (
                    <li key={r.id} className={"memo-d-row memo-tidy-row" + (focusRun === r.id ? " focus" : "")} data-tidy-run={r.id}>
                      <span className={"memo-d-state memo-tidy-state s-" + r.state}>{state ? tr(state) : tr("memo.tidy_state_unknown")}</span>
                      <span className="memo-d-at">{fmtDateTime(r.createdAt)}</span>
                      <span className="memo-d-tag">{tr(r.trigger === "manual" ? "memo.tidy_manual" : "memo.tidy_auto")}</span>
                      {counts.map((c) => (
                        <span key={c.key} className="memo-d-n">
                          {tr(c.key, c.params)}
                        </span>
                      ))}
                      {r.state === "undone" && r.undoneAt && <span className="memo-d-at">{fmtDateTime(r.undoneAt)}</span>}
                      {why && <span className="memo-d-held">{tr(why)}</span>}
                      {r.undoable && (
                        <Button small variant="ghost" title={tr("memo.tidy_undo_title")} disabled={undoing !== ""} onClick={() => void undo(r.id)}>
                          {undoing === r.id ? tr("memo.tidy_undo_busy") : tr("memo.tidy_undo")}
                        </Button>
                      )}
                      {groups.length > 0 && (
                        <div className="memo-tidy-blocked" role={refused[r.id] ? "alert" : undefined}>
                          {tr("memo.tidy_blocked_head")}{" "}
                          {groups.map((g) => (
                            <span key={g.why}>{tr(g.key, { count: g.count }) + " "}</span>
                          ))}
                        </div>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
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
