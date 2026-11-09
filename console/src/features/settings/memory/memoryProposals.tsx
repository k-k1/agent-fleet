// The memory consolidator's two screens (ADR 0130 decisions 6 and 7): its switch with the status of
// the last run (Agent memory > Settings), and the proposals it queued (Agent memory > Suggestions).
//
// Nothing here applies anything by itself. A proposal changes a memory only when the member clicks
// Approve on that one proposal and confirms; there is no approve-all, and a notification only
// opens this view. Every write goes through the Control Plane's signed relay (the member-origin
// mark, ADR 0113), so a session cannot decide a proposal. The list, detail and diff reuse the
// change list's layout (memoryChanges.tsx) on purpose.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Diff } from "../../scm/GitDiff.tsx";
import { Choice, OnOff, Row } from "../parts/controls.tsx";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import type {
  ConsolDelay,
  ConsolItemView,
  ConsolProposal,
  ConsolProposalView,
  ConsolStatus,
} from "./memoryTypes.ts";

const BASE = "api/agents/memory-consolidation";
const RUN_POLL_MS = 3000;

/** Go's Duration.String ("3h0m0s", "1m30.5s", "500ms") in milliseconds; 0 when it is not one. */
export function parseGoDuration(s: string | undefined): number {
  if (!s) return 0;
  const unit: Record<string, number> = { h: 3600e3, m: 60e3, s: 1e3, ms: 1 };
  let total = 0;
  for (const m of s.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)) total += parseFloat(m[1]) * unit[m[2]];
  return total;
}

/** A Go duration as "15 min" / "2 h" for the status line; the raw string when it is not one. */
export function humanDuration(s: string | undefined): string {
  const ms = parseGoDuration(s);
  if (!ms) return s ?? "";
  const min = Math.round(ms / 60e3);
  return min % 60 === 0 && min >= 60 ? `${min / 60} h` : `${min} min`;
}

/** A proposal with nothing the Agent can publish (every item is a note) is advice: it has no Approve. */
export const isAdviceOnly = (p: Pick<ConsolProposal, "items">): boolean =>
  !p.items.some((i) => i.op === "patch" || i.op === "create");

export type StatusFilter = "all" | "pending" | "decided" | "expired";
const DECIDED = new Set(["approved", "rejected", "refused", "partial"]);
export const matchesFilter = (status: string, f: StatusFilter): boolean =>
  f === "all" || (f === "decided" ? DECIDED.has(status) : status === f);

/** Retry reopens the observations of a proposal that did not end in a publish. */
export const canRetry = (status: string): boolean => status === "rejected" || status === "expired" || status === "refused" || status === "partial";

const delayCause = (d: ConsolDelay): string =>
  tMaybe("mem.cs_cause_" + d.cause.replace(/-/g, "_")) ?? d.cause;

/** The Agent's refusal of a decision as a sentence a member can act on. `code` is the Agent's
 *  error code (not_pending, stale, forgotten, exists, invalid, secret). */
export function refusalText(tr: ReturnType<typeof useT>, code: string | undefined, fallback: string): string {
  switch (code) {
    case "stale":
      return tr("mem.cs_err_stale");
    case "forgotten":
      return tr("mem.cs_err_forgotten");
    case "exists":
      return tr("mem.cs_err_exists");
    case "invalid":
      return tr("mem.cs_err_invalid");
    case "secret":
      return tr("mem.cs_err_secret");
    case "not_pending":
      return tr("mem.cs_err_not_pending");
    case "not_found":
      return tr("mem.cs_err_not_found");
    case "run_in_flight":
      return tr("mem.cs_err_run_in_flight");
    case "disabled":
      return tr("mem.cs_err_disabled");
  }
  return fallback;
}

// ConsolidationSettings is the switch and the last run's status (Agent memory > Settings). The
// switch is a ui-pref (memoryConsolidation, default off): turning it on sends memory text to a model
// and spends tokens while the workspace is idle, so it says so, and the Agent only accepts the
// change with the member-origin mark (the ui-prefs relay is signed).
export function ConsolidationSettings({ running, reload = 0 }: { running: boolean; reload?: number }) {
  const tr = useT();
  const s = useSettings();
  const enabled = s.memoryConsolidation;
  return (
    <>
      <Row label={tr("mem.cs_switch")}>
        <OnOff value={enabled} onChange={(v) => setSetting("memoryConsolidation", v)} />
      </Row>
      <p className="muted ds-note">{tr("mem.cs_switch_note")}</p>
      {enabled && !s.agentMemory && <p className="mem-warn">{tr("mem.cs_needs_memory")}</p>}
      {enabled && running && <ConsolidationStatus reload={reload} />}
    </>
  );
}

function ConsolidationStatus({ reload }: { reload: number }) {
  const tr = useT();
  const [st, setSt] = useState<ConsolStatus | null>(null);
  const [err, setErr] = useState(false);
  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api(BASE);
    if (signal.aborted) return true;
    if (res?.error) {
      setErr(true);
      return true;
    }
    setErr(false);
    setSt(res as ConsolStatus);
    return true;
  }, []);
  useRetryLoad(load, [reload]);
  const running = st?.run?.running === true;
  // While a run is in flight the status is re-read so the progress and the final report appear
  // without a page action; it stops by itself once the Agent says nothing is running.
  // A failed read does not stop it: the next poll clears the warning by itself, whereas stopping would
  // leave a stale "running" that only a reload could end. The interval is bounded and cleared on unmount.
  useEffect(() => {
    if (!running) return;
    const ac = new AbortController();
    const h = setInterval(() => void load(ac.signal), RUN_POLL_MS);
    return () => {
      ac.abort();
      clearInterval(h);
    };
  }, [running, load]);

  const [all, setAll] = useState(false);
  const [starting, setStarting] = useState(false);
  // setStarting is asynchronous state; the ref stops a second click that lands before it renders.
  const inFlight = useRef(false);
  const toast = useToast();
  const askConfirm = useConfirm();
  const runNow = async () => {
    if (!st || inFlight.current || running) return;
    const budget = st.config.tokenBudget;
    const ok = await askConfirm({
      title: tr("mem.cs_run_title"),
      body: (
        <>
          <p>{tr(all ? "mem.cs_run_body_all" : "mem.cs_run_body")}</p>
          <p>{budget ? tr("mem.cs_run_budget", { n: budget.toLocaleString() }) : tr("mem.cs_run_budget_unknown")}</p>
          <p className="muted">{tr("mem.cs_run_note")}</p>
        </>
      ),
      confirmLabel: tr("mem.cs_run_confirm"),
    });
    if (!ok || inFlight.current) return;
    inFlight.current = true;
    setStarting(true);
    try {
      // No id to repeat in the query: the CP audit row for this write has no target.
      const res = await apiJSON(`${BASE}/run`, "POST", all ? { all: true } : {});
      if (res?.error) toast(refusalText(tr, res.error.code, errDetail(res.error)));
      else toast(tr("mem.cs_run_started"), { kind: "success" });
    } catch {
      // The request may or may not have reached the Agent: the re-read below shows which.
      toast(tr("mem.af_failed"));
    } finally {
      inFlight.current = false;
      setStarting(false);
      await load(new AbortController().signal);
    }
  };
  if (err) return <p className="mem-warn">{tr("mem.cs_status_failed")}</p>;
  if (!st) return <p className="muted ds-note">{tr("common.loading")}</p>;
  const last = st.lastRun;
  const lastRun = st.trigger.lastRun ? Date.parse(st.trigger.lastRun) : NaN;
  const next = Number.isNaN(lastRun) ? NaN : lastRun + parseGoDuration(st.config.minInterval);
  const pending = st.counts.pending ?? 0;
  return (
    <div className="mem-cs-status" role="status">
      <h4>{tr("mem.cs_status_title")}</h4>
      <p>
        {last
          ? tr("mem.cs_last_run", {
              when: fmtDateTime(last.finished, DATETIME_FULL),
              status: tMaybe("mem.cs_run_" + last.status) ?? last.status,
            })
          : tr("mem.cs_never_run")}
        {last?.manual ? ` ${tr("mem.cs_last_manual")}` : ""}
        {last?.error ? ` — ${last.error}` : ""}
      </p>
      <p className="muted">
        {Number.isNaN(next)
          ? tr("mem.cs_next_any")
          : next <= Date.now()
            ? tr("mem.cs_next_due")
            : tr("mem.cs_next_at", { when: fmtDateTime(next, DATETIME_FULL) })}
        {" · "}
        {tr("mem.cs_next_rule", { idle: humanDuration(st.config.idleAfter) })}
      </p>
      <p>{tr("mem.cs_pending_count", { n: pending })}</p>
      <div className="mem-cs-run">
        {running ? (
          <p>
            {tr(st.run?.manual ? "mem.cs_running_manual" : "mem.cs_running_auto")}
            {(st.run?.total ?? 0) > 0 ? ` ${tr("mem.cs_running_progress", { n: Math.min((st.run?.done ?? 0) + 1, st.run!.total), m: st.run!.total })}` : ""}
          </p>
        ) : null}
        <button type="button" disabled={running || starting} onClick={() => void runNow()}>
          {tr("mem.cs_run_button")}
        </button>{" "}
        <label className="muted">
          <input type="checkbox" checked={all} disabled={running || starting} onChange={(e) => setAll(e.target.checked)} />{" "}
          {tr("mem.cs_run_all")}
        </label>
      </div>
      {st.waiting.length > 0 && (
        <>
          <p>{tr("mem.cs_delayed_title")}</p>
          <ul className="mem-findings">
            {st.waiting.map((d) => (
              <li key={d.topic}>
                <code>{d.memory || d.topic}</code> — {delayCause(d)}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

const statusLabel = (status: string): string => tMaybe("mem.cs_status_" + status) ?? status;

function ItemBlock({ iv }: { iv: ConsolItemView }) {
  const tr = useT();
  if (iv.op === "note") {
    return (
      <div className="mem-cs-item">
        <p>
          <span className="mem-badge">{tr("mem.cs_advice")}</span> <code>{iv.name}</code>{" "}
          <span className="muted">{tMaybe("mem.cs_advice_" + iv.advice) ?? iv.advice ?? ""}</span>
        </p>
        {iv.reason && <p>{iv.reason}</p>}
        {(iv.targets?.length ?? 0) > 0 && (
          <p className="muted">
            {tr("mem.cs_advice_targets")} {iv.targets!.map((n) => <code key={n}>{n} </code>)}
          </p>
        )}
        {/* Advice has no Approve: the engine has no call that writes a replacement, so a supersede or
            merge is applied by hand in Memories (#3696 builds the writer). */}
        <p className="muted ds-note">{tr("mem.cs_advice_note")}</p>
      </div>
    );
  }
  return (
    <div className="mem-cs-item">
      <p>
        <span className={"mem-badge af-op-" + iv.op}>{tMaybe("mem.cs_op_" + iv.op) ?? iv.op}</span> <code>{iv.name}</code>
        {iv.op === "patch" && iv.revision ? (
          <span className="muted"> {tr("mem.cs_at_revision", { n: iv.revision })}</span>
        ) : null}
        {iv.commit && <span className="muted"> · {tr("mem.cs_published")}</span>}
      </p>
      {iv.reason && <p>{iv.reason}</p>}
      {iv.stale && <p className="mem-warn">{tr("mem.cs_item_stale", { why: iv.stale })}</p>}
      {iv.diff ? <Diff text={iv.diff} embedded wrap /> : null}
    </div>
  );
}

function ProposalDetail({
  id,
  version,
  onDecided,
}: {
  id: string;
  version: number;
  onDecided: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [view, setView] = useState<ConsolProposalView | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // setBusy is asynchronous state; the ref stops a second click that lands before it renders.
  const inFlight = useRef(false);

  useEffect(() => {
    let live = true;
    setView(null);
    setErr("");
    api(`${BASE}/proposal?id=${encodeURIComponent(id)}`)
      .then((r) => {
        if (!live) return;
        if (r?.error) setErr(refusalText(tr, r.error.code, errDetail(r.error)));
        else setView(r as ConsolProposalView);
      })
      .catch(() => live && setErr(tr("mem.cs_load_failed")));
    return () => {
      live = false;
    };
  }, [id, version, tr]);

  // The id goes in the query as well as the body: the CP audit ledger reads the URL only.
  const send = (verb: "approve" | "reject" | "retry") =>
    apiJSON(`${BASE}/proposals/${verb}?proposal=${encodeURIComponent(id)}`, "POST", { id });

  // After any refusal the proposal is re-read: a decided or expired one must not keep its buttons.
  const refused = async (code: string | undefined, fallback: string) => {
    toast(refusalText(tr, code, fallback));
    onDecided();
  };

  const decide = async (verb: "approve" | "reject" | "retry") => {
    if (!view || inFlight.current) return;
    if (verb === "approve") {
      const targets = view.items.filter((i) => i.op === "patch" || i.op === "create");
      const ok = await askConfirm({
        title: tr("mem.cs_approve_title"),
        body: (
          <>
            <p>{tr("mem.cs_approve_body", { n: targets.length })}</p>
            <ul>
              {targets.map((i) => (
                <li key={i.op + i.name}>
                  <code>{i.name}</code> ({tMaybe("mem.cs_op_" + i.op) ?? i.op})
                </li>
              ))}
            </ul>
          </>
        ),
        confirmLabel: tr("mem.cs_approve"),
      });
      if (!ok) return;
    }
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      const res = await send(verb);
      if (res?.error) return await refused(res.error.code, errDetail(res.error));
      toast(verb === "approve" ? tr("mem.cs_done_approve") : verb === "reject" ? tr("mem.cs_done_reject") : tr("mem.cs_done_retry"), {
        kind: "success",
      });
      onDecided();
    } catch {
      // The request may or may not have reached the Agent: re-read what is there.
      toast(tr("mem.af_failed"));
      onDecided();
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  if (err) return <p className="mem-warn pad">{err}</p>;
  if (!view) return <p className="muted pad">{tr("common.loading")}</p>;
  const pending = view.status === "pending";
  const advice = isAdviceOnly(view);
  const stale = view.itemViews.some((i) => i.stale);
  return (
    <div className="mem-cs-detail">
      <div className="mem-diff-head">
        <span className={"mem-badge cs-" + view.status}>{statusLabel(view.status)}</span>
        {pending && !advice && (
          <button type="button" disabled={busy || stale} onClick={() => void decide("approve")}>
            {tr("mem.cs_approve")}
          </button>
        )}
        {pending && (
          <button type="button" disabled={busy} onClick={() => void decide("reject")}>
            {tr("mem.cs_reject")}
          </button>
        )}
        {canRetry(view.status) && (
          <button type="button" disabled={busy} onClick={() => void decide("retry")}>
            {tr("mem.cs_retry")}
          </button>
        )}
      </div>
      {advice && <p className="muted ds-note">{tr("mem.cs_advice_only")}</p>}
      {pending && stale && <p className="mem-warn">{tr("mem.cs_err_stale")}</p>}
      {view.status === "expired" && <p className="mem-warn">{tr("mem.cs_err_not_pending_expired")}</p>}
      {view.reason && (view.status === "refused" || view.status === "partial") && (
        <p className="mem-warn">{tr("mem.cs_refused_why", { why: view.reason })}</p>
      )}
      <p className="muted">
        <code>{view.memory}</code> · {view.scope === "user" ? tr("mem.af_scope_user") : view.projectId || ""}
        {" · "}
        {tr("mem.cs_created", { when: fmtDateTime(view.created, DATETIME_FULL) })}
        {pending && ` · ${tr("mem.cs_expires", { when: fmtDateTime(view.expires, DATETIME_FULL) })}`}
      </p>
      {view.targetRevisions && Object.keys(view.targetRevisions).length > 0 && (
        <p className="muted">
          {tr("mem.cs_target_revisions")}{" "}
          {Object.entries(view.targetRevisions).map(([n, r]) => (
            <code key={n}>
              {n}@{r}{" "}
            </code>
          ))}
        </p>
      )}
      {view.itemViews.map((iv, i) => (
        <ItemBlock key={i} iv={iv} />
      ))}
      <h5>{tr("mem.cs_evidence")}</h5>
      {view.evidence.length === 0 ? (
        <p className="muted">{tr("mem.cs_evidence_none")}</p>
      ) : (
        <ul className="mem-findings">
          {view.evidence.map((e) => (
            <li key={e.id}>
              <code>{e.source}</code> {e.rule && <code>{e.rule}</code>} {e.note}
            </li>
          ))}
        </ul>
      )}
      <p className="muted ds-note">{tr("mem.cs_observations", { n: view.observationIds.length })}</p>
    </div>
  );
}

export function ProposalsPanel({ reload, onChanged }: { reload: number; onChanged: () => void }) {
  const tr = useT();
  const [list, setList] = useState<ConsolProposal[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [filter, setFilter] = useState<StatusFilter>("pending");
  const [sel, setSel] = useState("");
  const [version, setVersion] = useState(0);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api(`${BASE}/proposals`);
    if (signal.aborted) return true;
    setLoadErr(res?.error ? errDetail(res.error) : "");
    setList(res?.error ? [] : (res?.proposals ?? []));
    return true;
  }, []);
  useRetryLoad(load, [reload, version]);

  const shown = useMemo(() => (list ?? []).filter((p) => matchesFilter(p.status, filter)), [list, filter]);
  useEffect(() => {
    setSel((cur) => (cur && shown.some((p) => p.id === cur) ? cur : (shown[0]?.id ?? "")));
  }, [shown]);

  const count = (f: StatusFilter) => (list ?? []).filter((p) => matchesFilter(p.status, f)).length;
  const filters: [StatusFilter, string][] = [
    ["pending", tr("mem.cs_filter_pending", { n: count("pending") })],
    ["decided", tr("mem.cs_filter_decided", { n: count("decided") })],
    ["expired", tr("mem.cs_filter_expired", { n: count("expired") })],
    ["all", tr("mem.cs_filter_all", { n: count("all") })],
  ];

  return (
    <div className="mem-changes mem-cs">
      <h4>{tr("mem.cs_title")}</h4>
      <p className="muted ds-hint">{tr("mem.cs_hint")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      <Choice value={filter} options={filters} onChange={(v) => setFilter(v as StatusFilter)} />
      <div className="mem-body">
        <ul className="mem-list">
          {list === null ? (
            <li className="muted pad">{tr("common.loading")}</li>
          ) : shown.length === 0 ? (
            !loadErr && <li className="muted pad">{tr("mem.cs_empty")}</li>
          ) : (
            shown.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  className={"mem-snap" + (p.id === sel ? " active" : "")}
                  aria-current={p.id === sel ? "true" : undefined}
                  onClick={() => setSel(p.id)}
                >
                  <span className="mem-snap-when">{fmtDateTime(p.created, DATETIME_FULL)}</span>
                  <span className={"mem-badge cs-" + p.status}>{statusLabel(p.status)}</span>
                  <span className="mem-af-name">{p.memory}</span>
                  <span className="mem-snap-what muted">
                    {isAdviceOnly(p) ? tr("mem.cs_advice") : tr("mem.cs_items", { n: p.items.length })}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
        <div className="mem-diff">
          {sel && (
            <ProposalDetail
              id={sel}
              version={version}
              onDecided={() => {
                setVersion((n) => n + 1);
                onChanged();
              }}
            />
          )}
        </div>
      </div>
    </div>
  );
}
