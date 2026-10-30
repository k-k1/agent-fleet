// AgentChangesPanel — the member's after-the-fact view of AF-owned agent memory (ADR 0108
// decision 8). Agents publish their writes without approval, so this is where a bad memory is
// found (who, when, what) and undone. Undoing never rewrites history: the Agent writes the
// earlier state back as a new change by the member.
//
// Only the newest change of a memory offers the way back; the Agent refuses an older one
// anyway, because undoing it would silently drop what came after.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Diff } from "../../scm/GitDiff.tsx";
import { Choice, OnOff, Row } from "../parts/controls.tsx";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { AutoSyncPanel, useFlipAgentMemory } from "./memoryAutoSync.tsx";
import { ExternalMemoryHelpLink } from "./ExternalMemoryHelpLink.tsx";
import { revertRun } from "./memoryReviewQueue.ts";
import type { ChangeDiff, MemoryChange, RunRevertReport, SecretFinding } from "./memoryTypes.ts";

// An unknown op from a newer Agent is printed raw rather than breaking the row.
export const opLabel = (op: string): string => tMaybe("mem.af_op_" + op) ?? op;

// AgentMemorySwitch is the sessions' switch (ui-prefs agentMemory, default off). It is a
// ui-pref, not Agent state, so it must stay reachable while the workspace is stopped: the member
// decides it for the next start, and this page is its only home.
export function AgentMemorySwitch() {
  const tr = useT();
  const enabled = useSettings().agentMemory;
  // The flip asks first, with the counts of what the Agent will import or write back by itself.
  const flip = useFlipAgentMemory();
  return (
    <>
      <Row label={tr("mem.af_switch")}>
        <OnOff value={enabled} onChange={(v) => void flip(v)} />
      </Row>
      <AutoSyncPanel />
      <p className="muted ds-note">{tr("mem.af_switch_note")}</p>
      {!enabled && <p className="muted ds-note">{tr("mem.af_switch_off_export")}</p>}
    </>
  );
}

// ExternalMemorySwitch is the outside-client setting (ui-prefs externalMemory, default off). Like
// the sessions' switch it is a ui-pref and stays reachable while the workspace is stopped.
export function ExternalMemorySwitch() {
  const tr = useT();
  const mode = useSettings().externalMemory;
  return (
    <>
      <Row label={tr("mem.ext_switch")}>
        <Choice
          value={mode}
          options={[
            ["off", tr("mem.ext_off")],
            ["read", tr("mem.ext_read")],
            ["write", tr("mem.ext_write")],
          ]}
          onChange={(v) => setSetting("externalMemory", v)}
        />
      </Row>
      <p className="muted ds-note">{tr("mem.ext_switch_note")}</p>
      <ExternalMemoryHelpLink />
    </>
  );
}

/** One change's diff as the Changes view shows it; the Memories view's per-memory history reuses
 *  it. The Agent scans the diff before it answers (the generic memory diff does not) and sends
 *  masked findings instead of a diff that fails. */
export function ChangeDiffBody({ commit }: { commit: string }) {
  const tr = useT();
  const [diff, setDiff] = useState<ChangeDiff | null>(null);
  useEffect(() => {
    let live = true;
    setDiff(null);
    api("api/agents/memory/entries/diff?commit=" + encodeURIComponent(commit))
      .then((d) => live && setDiff(d?.error ? { diff: "" } : d))
      .catch(() => live && setDiff({ diff: "" }));
    return () => {
      live = false;
    };
  }, [commit]);
  if (diff === null) return <pre className="diff muted">{tr("common.loading")}</pre>;
  if (diff.withheld)
    return (
      <div className="pad">
        <p className="mem-warn">{tr("mem.af_diff_withheld")}</p>
        <ul className="mem-findings">
          {(diff.findings ?? []).map((f, i) => (
            <li key={i}>
              <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
            </li>
          ))}
        </ul>
      </div>
    );
  return <Diff text={diff.diff} embedded wrap />;
}

/** Rows per page of the Changes view. */
const PAGE = 100;

export function AgentChangesPanel({ reload, onChanged }: { reload: number; onChanged: () => void }) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [changes, setChanges] = useState<MemoryChange[] | null>(null);
  // Rows the Agent left out because a value in them failed the scan; only the count is sent.
  const [withheld, setWithheld] = useState(0);
  const [loadErr, setLoadErr] = useState("");
  // The cursor of the next, older page ("" = the history ends here). `gen` drops the answer of a
  // "load older" that a reload has outdated.
  const [next, setNext] = useState("");
  const [moreBusy, setMoreBusy] = useState(false);
  const [moreErr, setMoreErr] = useState(false);
  const gen = useRef(0);
  const [sel, setSel] = useState("");
  const [busy, setBusy] = useState(false);
  const [mine, setMine] = useState(0);
  // What "revert this run" did, kept on screen: a conflict is the member's to look at.
  const [runReport, setRunReport] = useState<RunRevertReport | null>(null);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api(`api/agents/memory/entries/changes?limit=${PAGE}`);
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    gen.current++;
    setMoreBusy(false);
    setMoreErr(false);
    setNext(res?.error ? "" : (res?.next ?? ""));
    // A failed read is said, not shown as an empty history.
    setLoadErr(res?.error ? errText(res.error) : "");
    setChanges(res?.error ? [] : (res?.changes ?? []));
    setWithheld(res?.error ? 0 : (res?.withheld ?? 0));
    return true;
  }, []);
  useRetryLoad(load, [reload, mine]);

  // The next page is a commit cursor, so a change published meanwhile cannot repeat or skip a row.
  const loadOlder = async () => {
    const g = gen.current;
    setMoreBusy(true);
    setMoreErr(false);
    const res = await api(`api/agents/memory/entries/changes?limit=${PAGE}&before=${encodeURIComponent(next)}`).catch(
      () => ({ error: true }),
    );
    if (g !== gen.current) return;
    setMoreBusy(false);
    if (res?.error) return setMoreErr(true);
    const older: MemoryChange[] = res?.changes ?? [];
    setChanges((prev) => {
      const have = new Set((prev ?? []).map((c) => c.commit));
      return [...(prev ?? []), ...older.filter((c) => !have.has(c.commit))];
    });
    setWithheld((n) => n + (res?.withheld ?? 0));
    setNext(res?.next ?? "");
  };

  useEffect(() => {
    if (!changes?.length) {
      setSel("");
      return;
    }
    setSel((cur) => (cur && changes.some((c) => c.commit === cur) ? cur : changes[0].commit));
  }, [changes]);

  const where = (c: MemoryChange) => (c.scope === "user" ? tr("mem.af_scope_user") : (c.project?.display ?? ""));
  const who = (c: MemoryChange) =>
    c.authorKind === "member" ? tr("mem.af_by_member") : `${c.authorKind} · ${c.authorSession}`;

  // The commit also goes in the query: the CP audit ledger reads the URL only, never the body.
  const send = (c: MemoryChange, forget: boolean, ack: boolean) =>
    apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(c.commit), "POST", {
      commit: c.commit,
      forget,
      ack,
    });

  const act = async (c: MemoryChange, forget: boolean) => {
    const ok = await askConfirm({
      title: forget ? tr("mem.af_forget_title") : tr("mem.af_revert_title"),
      body: <p>{tr(forget ? "mem.af_forget_body" : "mem.af_revert_body", { name: c.name, where: where(c) })}</p>,
      confirmLabel: forget ? tr("mem.af_forget") : tr("mem.af_revert"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      let res = await send(c, forget, false);
      if (res?.error?.code === "memory_secret_detected") {
        // The earlier text trips the scan now. Only the member may let it through, and only
        // for this one request (ADR 0108 decision 9).
        const findings: SecretFinding[] = res.findings ?? [];
        const pass = await askConfirm({
          title: tr("mem.af_secret_title"),
          body: (
            <>
              <p>{tr("mem.af_secret_body")}</p>
              <ul className="mem-findings">
                {findings.map((f, i) => (
                  <li key={i}>
                    <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                  </li>
                ))}
              </ul>
            </>
          ),
          confirmLabel: tr("mem.af_secret_ack"),
          danger: true,
        });
        if (!pass) return;
        res = await send(c, forget, true);
      }
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(forget ? tr("mem.af_forgotten", { name: c.name }) : tr("mem.af_reverted", { name: c.name }), {
        kind: "success",
      });
      setMine((n) => n + 1);
      onChanged();
    } catch {
      // The request may or may not have reached the Agent: say so and re-read the list, which
      // shows what actually happened.
      toast(tr("mem.af_failed"));
      setMine((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  const selected = changes?.find((c) => c.commit === sel) ?? null;

  // Undoes everything the background review published in one run (ADR 0117 decision 5): each
  // commit that is still its memory's newest change, as a new change. An edited one is left and
  // reported below, never overwritten.
  const undoRun = async (c: MemoryChange) => {
    if (!c.reviewRun) return;
    const ok = await askConfirm({
      title: tr("mem.rv_revert_run_title"),
      body: <p>{tr("mem.rv_revert_run_body")}</p>,
      confirmLabel: tr("mem.rv_revert_run"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const res = await revertRun(c.reviewRun);
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      setRunReport(res as RunRevertReport);
      setMine((n) => n + 1);
      onChanged();
    } catch {
      toast(tr("mem.af_failed"));
      setMine((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mem-changes">
      <h4>{tr("mem.af_changes_title")}</h4>
      <p className="muted ds-hint">{tr("mem.af_changes_hint")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {withheld > 0 && <p className="mem-warn">{tr("mem.af_withheld", { n: withheld })}</p>}
      <div className="mem-body">
        <ul className="mem-list">
          {changes === null ? (
            <li className="muted pad">{tr("common.loading")}</li>
          ) : changes.length === 0 ? (
            !loadErr && withheld === 0 && <li className="muted pad">{tr("mem.af_empty")}</li>
          ) : (
            changes.map((c) => (
              <li key={c.commit}>
                <button
                  type="button"
                  className={"mem-snap" + (c.commit === sel ? " active" : "")}
                  aria-current={c.commit === sel ? "true" : undefined}
                  onClick={() => setSel(c.commit)}
                >
                  <span className="mem-snap-when">{fmtDateTime(c.at, DATETIME_FULL)}</span>
                  <span className={"mem-badge af-op-" + c.op}>{opLabel(c.op)}</span>
                  <span className="mem-af-name">{c.name}</span>
                  <span className="mem-snap-what muted">
                    {where(c)}
                    {" · "}
                    {who(c)}
                  </span>
                </button>
              </li>
            ))
          )}
          {moreErr && <li className="mem-warn pad">{tr("mem.af_changes_more_failed")}</li>}
          {next && (
            <li>
              <button type="button" className="mem-md-link" disabled={moreBusy} onClick={() => void loadOlder()}>
                {tr("mem.af_changes_more")}
              </button>
            </li>
          )}
        </ul>
        <div className="mem-diff">
          {selected && (
            <div className="mem-diff-head">
              <code title={selected.commit}>{selected.commit.slice(0, 9)}</code>
              {!selected.latest ? (
                <span className="muted">{tr("mem.af_not_latest")}</span>
              ) : (
                <>
                  {selected.purgedBefore ? (
                    <span className="muted">{tr("mem.pg_not_revertible")}</span>
                  ) : selected.revertible ? (
                    <button type="button" disabled={busy} onClick={() => void act(selected, false)}>
                      {tr("mem.af_revert")}
                    </button>
                  ) : (
                    <span className="muted">{tr("mem.af_not_revertible")}</span>
                  )}
                  {selected.live && (
                    <button type="button" disabled={busy} onClick={() => void act(selected, true)}>
                      {tr("mem.af_forget")}
                    </button>
                  )}
                </>
              )}
              {selected.reviewRun && (
                <button type="button" disabled={busy} onClick={() => void undoRun(selected)}>
                  {tr("mem.rv_revert_run")}
                </button>
              )}
            </div>
          )}
          {runReport && <RunReport report={runReport} onClose={() => setRunReport(null)} />}
          {selected && <ChangeDiffBody commit={selected.commit} />}
        </div>
      </div>
    </div>
  );
}

// RunReport says, per commit of the run, what reverting it did. A conflict is a memory someone
// edited after the review (or whose history cannot be restored): it is left as it is, and named.
export function RunReport({ report, onClose }: { report: RunRevertReport; onClose: () => void }) {
  const tr = useT();
  const count = (r: string) => report.items.filter((i) => i.result === r).length;
  const left = report.items.filter((i) => i.result === "conflict" || i.result === "failed");
  return (
    <div className="mem-run-report" role="status">
      <p>
        {tr("mem.rv_run_result", {
          done: count("reverted"),
          already: count("already"),
          left: left.length,
        })}
      </p>
      {left.length > 0 && (
        <ul>
          {left.map((i) => (
            <li key={i.commit}>
              <code>{i.name}</code>{" "}
              {i.result === "failed"
                ? tr("mem.rv_run_failed")
                : i.reason === "edited"
                  ? tr("mem.rv_run_edited")
                  : tr("mem.rv_run_blocked")}
            </li>
          ))}
        </ul>
      )}
      {left.length > 0 && <p className="muted ds-hint">{tr("mem.rv_run_again")}</p>}
      <button type="button" onClick={onClose}>
        {tr("common.close")}
      </button>
    </div>
  );
}
