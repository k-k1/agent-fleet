// The background memory review's proposals (ADR 0117 decision 6), under Settings > Agent memory >
// Changes > Proposed. Nothing the review suggests is in memory until the member approves it here:
// approve publishes it through the same revision check, secret scan and one commit as an agent's
// own save, reject deletes the text and keeps only a hash so the same idea is not proposed again.
//
// A proposal that cannot be approved (the memory changed since the review looked, was forgotten,
// or the proposal now matches a secret rule) is listed as such and can only be rejected; its text
// is not hidden from the member unless it matches a secret rule.

import { useCallback, useEffect, useState } from "react";
import { errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Diff } from "../../scm/GitDiff.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { opLabel } from "./memoryChanges.tsx";
import { approveProposal, listProposals, rejectAllProposals, rejectProposal } from "./memoryReviewQueue.ts";
import type { MemoryProposal } from "./memoryTypes.ts";

// The stale reasons the Agent can send; an unknown one from a newer Agent is shown as "changed".
const STALE_KEYS: Record<string, MsgKey> = {
  changed: "mem.rv_stale_changed",
  forgotten: "mem.rv_stale_forgotten",
  secret: "mem.rv_stale_secret",
  invalid: "mem.rv_stale_invalid",
};

export function ProposedPanel({
  reload,
  poll = 0,
  onChanged,
}: {
  reload: number;
  /** Moves when the status was read again (the queue may have changed in the background, or in
   *  another tab), which re-reads the list. */
  poll?: number;
  onChanged: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [items, setItems] = useState<MemoryProposal[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [sel, setSel] = useState("");
  const [busy, setBusy] = useState(false);
  const [mine, setMine] = useState(0);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await listProposals();
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    // A refresh that fails keeps what is on screen, with the failure said, rather than blanking a
    // list the member may be in the middle of; a first read that fails shows no rows.
    setItems((prev) => (res?.error ? (prev ?? []) : (res?.proposals ?? [])));
    return true;
  }, []);
  useRetryLoad(load, [reload, mine, poll]);

  useEffect(() => {
    if (!items?.length) {
      setSel("");
      return;
    }
    setSel((cur) => (cur && items.some((p) => p.id === cur) ? cur : items[0].id));
  }, [items]);

  const selected = items?.find((p) => p.id === sel) ?? null;
  const where = (p: MemoryProposal) => (p.scope === "user" ? tr("mem.af_scope_user") : (p.project ?? ""));
  const staleText = (p: MemoryProposal) => tr(STALE_KEYS[p.staleReason ?? ""] ?? STALE_KEYS.changed);
  // Re-reads the list and the count, and tells the changes list when something was published.
  const done = (published: boolean) => {
    setMine((n) => n + 1);
    onChanged();
    if (published) toast(tr("mem.rv_approved"), { kind: "success" });
  };

  const approve = async (p: MemoryProposal) => {
    setBusy(true);
    try {
      const res = await approveProposal(p.id);
      if (res?.error) {
        // A refusal changes what the list should say (stale, forgotten): read it again.
        toast(errDetail(res.error));
        setMine((n) => n + 1);
        return;
      }
      done(true);
    } catch {
      // The request may or may not have reached the Agent: say so and read the queue again, which
      // shows what actually happened.
      toast(tr("mem.af_failed"));
      setMine((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  const reject = async (p: MemoryProposal) => {
    setBusy(true);
    try {
      const res = await rejectProposal(p.id);
      if (res?.error) {
        toast(errDetail(res.error));
        setMine((n) => n + 1);
        return;
      }
      toast(tr("mem.rv_rejected"), { kind: "success" });
      done(false);
    } catch {
      toast(tr("mem.af_failed"));
      setMine((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  const rejectAll = async (run: string) => {
    const n = (items ?? []).filter((p) => !run || p.runId === run).length;
    const ok = await askConfirm({
      title: tr("mem.rv_reject_all_title"),
      body: <p>{tr(run ? "mem.rv_reject_run_body" : "mem.rv_reject_all_body", { n })}</p>,
      confirmLabel: tr(run ? "mem.rv_reject_run" : "mem.rv_reject_all", { n }),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const res = await rejectAllProposals(run);
      if (res?.error) {
        toast(errDetail(res.error));
      } else if (res?.failed) {
        toast(tr("mem.rv_reject_partial", { done: res.rejected ?? 0, failed: res.failed }));
      } else {
        toast(tr("mem.rv_rejected_n", { n: res?.rejected ?? 0 }), { kind: "success" });
      }
      done(false);
    } catch {
      toast(tr("mem.af_failed"));
      setMine((x) => x + 1);
    } finally {
      setBusy(false);
    }
  };

  const runsInList = new Set((items ?? []).map((p) => p.runId));

  return (
    <div className="mem-changes">
      <p className="muted ds-hint">{tr("mem.rv_hint")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {items !== null && items.length > 0 && (
        <div className="mem-prop-bulk">
          {selected && runsInList.size > 1 && (
            <button type="button" disabled={busy} onClick={() => void rejectAll(selected.runId)}>
              {tr("mem.rv_reject_run")}
            </button>
          )}
          <button type="button" disabled={busy} onClick={() => void rejectAll("")}>
            {tr("mem.rv_reject_all", { n: items.length })}
          </button>
        </div>
      )}
      <div className="mem-body">
        <ul className="mem-list">
          {items === null ? (
            <li className="muted pad">{tr("common.loading")}</li>
          ) : items.length === 0 ? (
            !loadErr && <li className="muted pad">{tr("mem.rv_empty")}</li>
          ) : (
            items.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  className={"mem-snap" + (p.id === sel ? " active" : "")}
                  aria-current={p.id === sel ? "true" : undefined}
                  onClick={() => setSel(p.id)}
                >
                  <span className="mem-snap-when">{fmtDateTime(p.created, DATETIME_FULL)}</span>
                  <span className={"mem-badge af-op-" + p.op}>{opLabel(p.op)}</span>
                  <span className="mem-af-name">{p.withheld ? tr("mem.rv_withheld_name") : p.name}</span>
                  <span className="mem-snap-what muted">
                    {p.withheld ? "" : where(p)}
                    {p.state === "stale" ? (p.withheld ? "" : " · ") + tr("mem.rv_stale_badge") : ""}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
        <div className="mem-diff">
          {selected && (
            <div className="mem-diff-head mem-prop-head">
              {selected.state === "pending" && !selected.withheld ? (
                <button type="button" disabled={busy} onClick={() => void approve(selected)}>
                  {tr("mem.rv_approve")}
                </button>
              ) : (
                <span className="mem-warn">{selected.withheld ? tr(STALE_KEYS.secret) : staleText(selected)}</span>
              )}
              <button type="button" disabled={busy} onClick={() => void reject(selected)}>
                {tr("mem.rv_reject")}
              </button>
              <span className="muted">{tr("mem.rv_from_session", { session: selected.session })}</span>
            </div>
          )}
          {selected && !selected.withheld && (
            <>
              <p className="mem-prop-why">
                <strong>{tr("mem.rv_why")}</strong> {selected.reason}
              </p>
              <Diff text={selected.diff} embedded wrap />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
