// LintPanel — the read-only lint report over AF memory (ADR 0130 decision 5, step 1). One scope
// at a time: the Agent checks each body for links to no memory, paths / symbols / ADR numbers
// missing from the project's default branch (read by ref, no working copy touched), retired
// terms and bodies over 4,096 bytes, and reports. Nothing is written to any memory. The only
// thing the member edits here is the retired-term list, which is a setting of the report.

import { useCallback, useEffect, useState } from "react";
import {
  api,
  apiJSON,
  errDetail,
  errText,
  isTransientErr,
} from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useEscLayer } from "../../../lib/escLayer.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import type { LintReport, ProjectRow } from "./memoryTypes.ts";

const CHECK_LABEL: Record<string, MsgKey> = {
  "broken-link": "mem.lint_check_broken-link",
  "missing-path": "mem.lint_check_missing-path",
  "missing-symbol": "mem.lint_check_missing-symbol",
  "missing-adr": "mem.lint_check_missing-adr",
  "retired-term": "mem.lint_check_retired-term",
  oversize: "mem.lint_check_oversize",
};
const CHECK_ORDER = Object.keys(CHECK_LABEL);
const USER = "user";

export function LintPanel({
  reload,
  onClose,
}: {
  reload: number;
  onClose?: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  useEscLayer(() => onClose?.(), !!onClose);
  const [rows, setRows] = useState<ProjectRow[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  // "user" or a project id.
  const [scope, setScope] = useState(USER);
  const [report, setReport] = useState<LintReport | null>(null);
  const [reportErr, setReportErr] = useState("");
  const [again, setAgain] = useState(0);
  const [terms, setTerms] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  // The list as stored, and the proposals the member turned down. An accept or a reject posts
  // these, never the textarea, so an unsaved edit there is not saved by accident.
  const [stored, setStored] = useState<string[]>([]);
  const [rejected, setRejected] = useState<string[]>([]);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/entries/projects");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    setRows(res?.error ? [] : (res?.projects ?? []));
    const t = await api("api/agents/memory/entries/lint-terms");
    if (signal.aborted) return true;
    if (isTransientErr(t)) return false;
    setTerms(t?.error ? "" : ((t?.terms ?? []) as string[]).join("\n"));
    setStored(t?.error ? [] : ((t?.terms ?? []) as string[]));
    setRejected(t?.error ? [] : ((t?.rejected ?? []) as string[]));
    return true;
  }, []);
  useRetryLoad(load, [reload]);

  // Only the latest request may answer, so a slow report for an earlier scope never replaces the
  // one chosen now.
  useEffect(() => {
    if (rows === null) return;
    let live = true;
    setReport(null);
    setReportErr("");
    const q =
      scope === USER
        ? "scope=user"
        : `scope=project&project=${encodeURIComponent(scope)}`;
    api("api/agents/memory/entries/lint?" + q)
      .then((res) => {
        if (!live) return;
        if (res?.error) setReportErr(errDetail(res.error));
        else if (!res?.report) setReportErr(tr("mem.af_failed"));
        else setReport(res as LintReport);
      })
      .catch(() => live && setReportErr(tr("mem.af_failed")));
    return () => {
      live = false;
    };
  }, [rows, scope, again, tr]);

  const saveTerms = async () => {
    if (terms === null) return;
    setSaving(true);
    try {
      const res = await apiJSON(
        "api/agents/memory/entries/lint-terms",
        "POST",
        { terms: terms.split("\n") },
      );
      if (res?.error) {
        toast(errDetail(res.error), { kind: "error" });
        return;
      }
      setTerms(((res?.terms ?? []) as string[]).join("\n"));
      setStored((res?.terms ?? []) as string[]);
      toast(tr("mem.lint_terms_saved"), { kind: "success" });
      setAgain((n) => n + 1);
    } finally {
      setSaving(false);
    }
  };

  // Accepting appends the proposal to the member's list; rejecting records it so it is not
  // proposed again. Nothing else ever adds to either.
  const decide = async (term: string, accept: boolean) => {
    setSaving(true);
    try {
      const res = await apiJSON(
        "api/agents/memory/entries/lint-terms",
        "POST",
        {
          terms: accept ? [...stored, term] : stored,
          rejected: accept ? rejected : [...rejected, term],
        },
      );
      if (res?.error) {
        toast(errDetail(res.error), { kind: "error" });
        return;
      }
      setStored((res?.terms ?? []) as string[]);
      setRejected((res?.rejected ?? []) as string[]);
      if (accept) setTerms(((res?.terms ?? []) as string[]).join("\n"));
      toast(tr(accept ? "mem.lint_terms_saved" : "mem.lint_prop_rejected"), {
        kind: "success",
      });
      setAgain((n) => n + 1);
    } finally {
      setSaving(false);
    }
  };

  const projects = (rows ?? []).filter((r) => r.count > 0 || r.withheld);
  const grouped = CHECK_ORDER.map((c) => ({
    check: c,
    items: (report?.report.findings ?? []).filter((f) => f.check === c),
  })).filter((g) => g.items.length > 0);

  return (
    <section className="mem-wiz" aria-label={tr("mem.lint_title")}>
      <h4>{tr("mem.lint_title")}</h4>
      <p className="muted ds-hint">{tr("mem.lint_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      <label className="mem-scope">
        <span className="muted">{tr("mem.lint_scope")}</span>
        <select value={scope} onChange={(e) => setScope(e.target.value)}>
          <option value={USER}>{tr("mem.lint_scope_user")}</option>
          {projects.map((r) => (
            <option key={r.project.id} value={r.project.id}>
              {r.project.display} ({r.count})
            </option>
          ))}
        </select>{" "}
        <Button onClick={() => setAgain((n) => n + 1)}>
          {tr("mem.lint_rerun")}
        </Button>
      </label>
      {reportErr && <p className="mem-warn">{reportErr}</p>}
      {!report && !reportErr && <p className="muted">{tr("common.loading")}</p>}
      {report && (
        <div className="mem-lint-report" aria-live="polite">
          <p>
            {tr("mem.lint_summary", {
              n: report.report.checked,
              f: report.report.findings.length,
            })}
            {report.ref && (
              <span className="muted">
                {" "}
                {tr("mem.lint_ref", {
                  ref: report.ref,
                  commit: report.commit ?? "",
                })}
              </span>
            )}
          </p>
          {(report.withheld ?? 0) > 0 && (
            <p className="mem-warn">
              {tr("mem.lint_withheld", { n: report.withheld ?? 0 })}
            </p>
          )}
          {report.report.skipped?.map((s) => (
            <p key={s} className="muted ds-hint">
              {tr("mem.lint_skipped", { what: s })}
            </p>
          ))}
          {grouped.length === 0 && report.report.checked > 0 && (
            <p className="muted">{tr("mem.lint_clean")}</p>
          )}
          {grouped.map((g) => (
            <details key={g.check} className="mem-ci-group" open>
              <summary>
                {tr(CHECK_LABEL[g.check])}{" "}
                <span className="muted">({g.items.length})</span>
              </summary>
              <ul className="mem-lint-list">
                {g.items.map((f, i) => (
                  <li key={`${f.memory}:${f.subject}:${i}`}>
                    <code>{f.memory}</code>
                    {f.line ? (
                      <span className="muted">:{f.line}</span>
                    ) : null} —{" "}
                    {f.check === "oversize" ? (
                      tr("mem.lint_bytes", { n: f.subject })
                    ) : (
                      <code>{f.subject}</code>
                    )}
                  </li>
                ))}
              </ul>
            </details>
          ))}
        </div>
      )}
      {(report?.report.proposals?.length ?? 0) > 0 && (
        <div className="mem-lint-proposals">
          <h5>{tr("mem.lint_prop_title")}</h5>
          <p className="muted ds-hint">{tr("mem.lint_prop_hint")}</p>
          <ul className="mem-lint-list">
            {report!.report.proposals!.map((p) => (
              <li key={p.term}>
                <code>{p.term}</code> —{" "}
                <span className="muted">{p.evidence}</span>{" "}
                <span className="muted">
                  {tr("mem.lint_prop_used", { list: p.memories.join(", ") })}
                </span>{" "}
                <Button
                  disabled={saving}
                  onClick={() => void decide(p.term, true)}
                >
                  {tr("mem.lint_prop_accept")}
                </Button>{" "}
                <Button
                  disabled={saving}
                  onClick={() => void decide(p.term, false)}
                >
                  {tr("mem.lint_prop_reject")}
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}
      <h5>{tr("mem.lint_terms_title")}</h5>
      <p className="muted ds-hint">{tr("mem.lint_terms_hint")}</p>
      <textarea
        className="mem-lint-terms"
        rows={5}
        aria-label={tr("mem.lint_terms_title")}
        value={terms ?? ""}
        disabled={terms === null || saving}
        onChange={(e) => setTerms(e.target.value)}
      />
      <div className="mem-ci-actions">
        <Button
          disabled={terms === null || saving}
          onClick={() => void saveTerms()}
        >
          {tr("common.save")}
        </Button>
      </div>
      {onClose && (
        <div className="mem-ci-actions mem-wiz-foot">
          <Button icon="arrow-left" onClick={onClose}>
            {tr("mem.wz_back")}
          </Button>
        </div>
      )}
    </section>
  );
}
