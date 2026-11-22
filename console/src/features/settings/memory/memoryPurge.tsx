// PurgeHistoryDialog — the member's purge of one memory's past revisions from the 0022 history
// (#1982, ADR 0108 open question 3). Forget only hides a memory; this rewrites af-memory.git so the
// text is gone from every ref and from disk. The dialog first asks the Agent what would be
// rewritten (purge-preview: counts, refs, the secret scanner's masked findings, never the text) and
// asks for the memory's name to be typed before the purge route is called. The CP signs and audits
// the purge; no MCP tool reaches it.

import { useState } from "react";
import { errDetail } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { useApiOnce } from "../parts/useApiLoad.ts";
import { getPurgePreview, purgeMemory } from "./memoryApi.ts";
import { Modal } from "../../../ui/Modal.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import type { PurgePreview } from "./memoryTypes.ts";

export interface PurgeTarget {
  scope: string;
  project?: { id: string };
  name: string;
  /** The scope as the list names it, for the heading. */
  where: string;
}

const REF_KEY = {
  main: "mem.pg_ref_main",
  branch: "mem.pg_ref_branch",
  import: "mem.pg_ref_import",
  rescue: "mem.pg_ref_rescue",
} as const;

const projectId = (t: PurgeTarget) => (t.scope === "user" ? "" : (t.project?.id ?? ""));

export function PurgeHistoryDialog({
  target,
  onClose,
  onDone,
}: {
  target: PurgeTarget;
  onClose: () => void;
  onDone: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [pv, setPv] = useState<PurgePreview | null>(null);
  const [err, setErr] = useState("");
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);

  useApiOnce(
    () => getPurgePreview(target.scope, projectId(target), target.name),
    (res) => {
      if (!isOk(res)) setErr(errDetail(res.error));
      else setPv({ changes: [], refs: [], findings: [], changesTotal: 0, snapshots: 0, ...res } as PurgePreview);
    },
    () => setErr(tr("mem.af_failed")),
    [target, tr],
  );

  const run = async () => {
    if (!pv || typed !== target.name) return;
    setBusy(true);
    try {
      // The project also goes in the query: the CP audit ledger reads the URL only, never the body.
      const res = await purgeMemory(projectId(target), {
        scope: target.scope,
        project: projectId(target),
        name: target.name,
        plan: pv.plan,
        confirm: typed,
      });
      if (!isOk(res)) {
        setErr(errDetail(res.error));
        return;
      }
      toast(tr("mem.pg_done", { name: target.name }), { kind: "success" });
      onDone();
      onClose();
    } catch {
      setErr(tr("mem.af_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("mem.pg_title", { name: target.name })} onClose={onClose} lockClose={busy} className="mem-purge">
      <div className="ui-modal-body">
        <p>{tr("mem.pg_intro", { name: target.name, where: target.where })}</p>
        {err && <p className="mem-warn">{err}</p>}
        {pv === null && !err && <p className="muted">{tr("common.loading")}</p>}
        {pv?.nothing && <p className="muted">{tr("mem.pg_nothing")}</p>}
        {pv && !pv.nothing && (
          <>
            <p>{pv.keepsCurrent ? tr("mem.pg_keeps_current") : tr("mem.pg_keeps_nothing")}</p>
            <ul className="mem-purge-facts">
              <li>{tr("mem.pg_changes", { n: pv.changesTotal })}</li>
              <li>{tr("mem.pg_snapshots", { n: pv.snapshots })}</li>
              {pv.refs
                .filter((r) => r.affected > 0)
                .map((r) => (
                  <li key={r.name}>
                    {tr(REF_KEY[r.kind] ?? "mem.pg_ref_branch", { n: r.affected })} <code>{r.name}</code>
                  </li>
                ))}
            </ul>
            {pv.changes.length > 0 && (
              <details>
                <summary>{tr("mem.pg_list")}</summary>
                <ul>
                  {pv.changes.map((c) => (
                    <li key={c.commit}>
                      <code>{c.commit.slice(0, 9)}</code> {fmtDateTime(c.at, DATETIME_FULL)} · {c.op} · {c.authorKind}
                    </li>
                  ))}
                </ul>
              </details>
            )}
            {pv.findings.length > 0 && (
              <>
                <p className="mem-warn">{tr("mem.pg_findings")}</p>
                <ul className="mem-findings">
                  {pv.findings.map((f, i) => (
                    <li key={i}>
                      <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                    </li>
                  ))}
                </ul>
              </>
            )}
            <p className="mem-warn">{tr("mem.pg_bundles")}</p>
            <p className="muted">{tr("mem.pg_irreversible")}</p>
            <label>
              {tr("mem.pg_type", { name: target.name })}
              <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
            </label>
          </>
        )}
        <div className="mem-br-actions">
          <Button variant="ghost" disabled={busy} onClick={onClose}>
            {tr("mem.br_cancel")}
          </Button>
          <Button variant="danger" disabled={busy || !pv || pv.nothing || typed !== target.name} onClick={() => void run()}>
            {tr("mem.pg_run")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
