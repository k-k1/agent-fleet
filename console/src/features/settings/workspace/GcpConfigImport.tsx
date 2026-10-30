// GcpConfigImport — "Import from gcloud configurations" on the Google Cloud tab (issue #3017,
// part 2), modelled on AwsConfigImport.
//
// The files are read here, in the browser, and never sent anywhere: what reaches the CP is the
// profile fields of the rows the member ticks, through the same POST the add form uses. The
// pasted text is dropped as soon as it has been read (and on cancel); a path or token file named
// in a configuration is never shown or sent, because only the six mapped fields leave the
// reader.
import { useMemo, useRef, useState } from "react";
import { Modal } from "../../../ui/Modal.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import {
  MAX_GCP_FILES,
  MAX_GCP_FILE_BYTES,
  configNameOfFile,
  planGcpCreates,
  readGcloudSource,
  resolveGcpImport,
  safeGcpKey,
  type GcpImportRow,
  type GcpReason,
  type GcpRowEdit,
  type NativeGcpConfig,
} from "../../../lib/gcloudConfigImport.ts";
import { executeGcpPlan, loadGcpProfiles, type GcpExecRow } from "./gcpImportExec.ts";

type Tr = ReturnType<typeof useT>;

export function gcpReasonText(tr: Tr, r: GcpReason): string {
  switch (r.code) {
    case "parse":
      // A key name is file text too: only one this importer knows is shown.
      return tr(("gcp.imp_err_" + r.error.code) as MsgKey, { line: r.error.line, key: safeGcpKey(r.error.key ?? "") || "?" });
    case "invalid":
    case "auth_override":
    case "universe":
      return tr(("gcp.imp_r_" + r.code) as MsgKey, { key: r.key || "?" });
    case "label_taken":
    case "label_dup":
      return tr(("gcp.imp_r_" + r.code) as MsgKey, { name: r.other });
    default:
      return tr(("gcp.imp_r_" + r.code) as MsgKey);
  }
}

// Reasons that come from the file itself; renaming cannot fix them.
const FILE_REASONS = new Set(["not_config", "bad_name", "parse", "no_project", "invalid", "key_material", "sa_account", "delegation", "auth_override", "endpoint_override", "proxy", "universe", "interpolation"]);

export function GcpConfigImport({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const tr = useT();
  const fileRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [pasteName, setPasteName] = useState("default");
  const [err, setErr] = useState("");
  const [cfgs, setCfgs] = useState<NativeGcpConfig[] | null>(null);
  const [existing, setExisting] = useState<any[]>([]);
  const [edits, setEdits] = useState<Record<number, GcpRowEdit>>({});
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<[number, number] | null>(null);
  const [results, setResults] = useState<GcpExecRow[] | null>(null);
  const cancelRef = useRef(false);

  const preview = useMemo(() => (cfgs ? resolveGcpImport(cfgs, existing, edits) : null), [cfgs, existing, edits]);
  const creating = preview ? preview.rows.filter((r) => r.status === "create").length : 0;
  const ignored = preview ? preview.rows.reduce((n, r) => n + (r.reason ? 0 : r.ignored), 0) : 0;
  const bom = !!cfgs?.some((c) => c.bomStripped);

  const edit = (index: number, e: GcpRowEdit) => setEdits((all) => ({ ...all, [index]: { ...all[index], ...e } }));

  // Sources: [name, text] pairs; a null name is a file that is not a configuration.
  const read = async (sources: [string | null, string][]) => {
    setErr("");
    setResults(null);
    // The raw text is not kept past this point, whatever the outcome.
    setText("");
    if (fileRef.current) fileRef.current.value = "";
    if (sources.length > MAX_GCP_FILES) {
      setErr(tr("gcp.imp_too_many", { max: MAX_GCP_FILES }));
      return;
    }
    if (sources.some(([, t]) => new TextEncoder().encode(t).byteLength > MAX_GCP_FILE_BYTES)) {
      setErr(tr("gcp.imp_too_big"));
      return;
    }
    const native = sources.map(([name, t], i) => readGcloudSource(i, name, t));
    setLoading(true);
    try {
      const rows = await loadGcpProfiles();
      if (!rows) {
        setErr(tr("gcp.imp_unread"));
        return;
      }
      setExisting(rows);
      setEdits({});
      setCfgs(native);
    } finally {
      setLoading(false);
    }
  };

  const readFiles = async (files: File[]) => {
    // A file over the cap is not read at all.
    if (files.length > MAX_GCP_FILES) {
      setErr(tr("gcp.imp_too_many", { max: MAX_GCP_FILES }));
      return;
    }
    if (files.some((f) => f.size > MAX_GCP_FILE_BYTES)) {
      setErr(tr("gcp.imp_too_big"));
      return;
    }
    const texts = await Promise.all(files.map((f) => f.text()));
    await read(files.map((f, i) => [configNameOfFile(f.name), texts[i]]));
  };

  const apply = async () => {
    if (!preview) return;
    setBusy(true);
    cancelRef.current = false;
    try {
      // Re-read what exists now, so a retry after a partial run, or a change made in another
      // tab, is planned against the truth rather than the list the preview was built on.
      const rows = await loadGcpProfiles();
      if (!rows) {
        setErr(tr("gcp.imp_unread"));
        return;
      }
      const plan = planGcpCreates(resolveGcpImport(cfgs!, rows, edits).rows);
      setProgress([0, plan.length]);
      const done = await executeGcpPlan(plan, {
        cancelled: () => cancelRef.current,
        onProgress: (n, total) => setProgress([n, total]),
      });
      setResults(done);
      onImported();
      const after = await loadGcpProfiles();
      if (after) setExisting(after);
    } finally {
      setBusy(false);
      setProgress(null);
    }
  };

  const close = () => {
    if (busy) {
      cancelRef.current = true;
      return;
    }
    setText("");
    setCfgs(null);
    onClose();
  };

  return (
    // No lockClose: it makes the whole panel inert, Stop included. close() cancels instead
    // while a run is in flight.
    <Modal title={tr("gcp.imp_title")} onClose={close} className="aws-import">
      <div className="ui-modal-body">
        <p className="field-help">{tr("gcp.imp_intro")}</p>
        {!cfgs && (
          <div className="aws-import-input">
            <input
              ref={fileRef}
              type="file"
              multiple
              className="cinput"
              disabled={loading}
              aria-label={tr("gcp.imp_file")}
              onChange={(e) => {
                const files = [...(e.target.files ?? [])];
                if (files.length) void readFiles(files);
              }}
            />
            <textarea
              className="cinput mono"
              rows={8}
              value={text}
              spellCheck={false}
              autoComplete="off"
              placeholder={tr("gcp.imp_paste")}
              aria-label={tr("gcp.imp_paste")}
              onChange={(e) => setText(e.target.value)}
            />
            <label className="ssm-fld">
              <span>{tr("gcp.imp_name")}</span>
              <input className="cinput" value={pasteName} aria-label={tr("gcp.imp_name")} onChange={(e) => setPasteName(e.target.value.trim())} />
            </label>
            <div className="flow">
              <button
                type="button"
                className="primary"
                disabled={!text.trim() || loading}
                onClick={() => void read([[pasteName, text]])}
              >
                {tr("gcp.imp_read")}
              </button>
            </div>
          </div>
        )}
        {err && (
          <p className="ssm-load-err" role="alert">
            {err}
          </p>
        )}
        {cfgs && preview && (
          <>
            {bom && <p className="muted">{tr("gcp.imp_bom")}</p>}
            {preview.rows.length === 0 ? (
              <p className="muted">{tr("gcp.imp_none")}</p>
            ) : (
              <ul className="ssm-list aws-import-rows">
                {preview.rows.map((r) => (
                  <Row key={r.index} tr={tr} r={r} e={edits[r.index] || {}} edit={edit} busy={busy} />
                ))}
              </ul>
            )}
            <p className="field-help">
              {tr("gcp.imp_summary", { create: creating })}
              {ignored > 0 && " " + tr("gcp.imp_ignored", { n: ignored })}
            </p>
            <p className="field-help">{tr("gcp.imp_after")}</p>
            {results && (
              <ul className="backup-result aws-import-results">
                {results.map((r, i) => (
                  <li key={i} className={r.outcome}>
                    {tr(("gcp.imp_o_" + r.outcome) as MsgKey, { label: r.label, code: r.code ?? "" })}
                  </li>
                ))}
              </ul>
            )}
            <div className="flow">
              <button type="button" className="primary" disabled={busy || creating === 0} onClick={() => void apply()}>
                {progress ? tr("gcp.imp_running", { done: progress[0], total: progress[1] }) : tr("gcp.imp_do", { n: creating })}
              </button>
              <button type="button" className="ghost" onClick={busy ? () => (cancelRef.current = true) : () => setCfgs(null)}>
                {busy ? tr("gcp.imp_stop") : tr("gcp.imp_other_file")}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function Row({ tr, r, e, edit, busy }: { tr: Tr; r: GcpImportRow; e: GcpRowEdit; edit: (index: number, e: GcpRowEdit) => void; busy: boolean }) {
  const editable = !r.reason || !FILE_REASONS.has(r.reason.code);
  const shown = r.display || tr("gcp.imp_unnamed", { n: r.index + 1 });
  const status =
    r.status === "create"
      ? tr("gcp.imp_s_create")
      : r.status === "exists"
        ? tr("gcp.imp_s_exists")
        : r.status === "unselected"
          ? tr("gcp.imp_s_unselected")
          : tr("gcp.imp_s_blocked", { reason: gcpReasonText(tr, r.reason!) });
  return (
    <li className={"ssm-item aws-import-row " + r.status} data-row={r.display || "file-" + (r.index + 1)}>
      <div className="ssm-item-head">
        <label className="aws-import-pick">
          <input
            type="checkbox"
            checked={r.status !== "exists" && editable && r.selected}
            disabled={busy || r.status === "exists" || !editable}
            aria-label={shown}
            onChange={(ev) => edit(r.index, { selected: ev.target.checked })}
          />
          <span className="ssm-alias">{shown}</span>
        </label>
      </div>
      <div className="aws-import-status">{status}</div>
      {editable && r.status !== "exists" && (
        <div className="ssm-fgrid">
          <label className="ssm-fld">
            <span>{tr("gcp.imp_label")}</span>
            <input className="cinput" value={e.label ?? r.display ?? ""} disabled={busy} onChange={(ev) => edit(r.index, { label: ev.target.value })} />
          </label>
        </div>
      )}
      {r.fields && (r.status === "create" || r.status === "exists") && (
        <div className="ssm-meta muted">
          <span>{r.fields.project}</span>
          <span>{r.fields.account || tr("gcp.imp_meta_account_login")}</span>
          {r.fields.impersonateServiceAccount && <span>{r.fields.impersonateServiceAccount}</span>}
          {r.fields.region && <span>{r.fields.region}</span>}
          {r.fields.zone && <span>{r.fields.zone}</span>}
        </div>
      )}
    </li>
  );
}
