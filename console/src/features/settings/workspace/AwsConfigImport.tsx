// AwsConfigImport — "Import from ~/.aws/config" on the AWS profiles/SSM tab (issue #3010).
//
// The file is read here, in the browser, and never sent anywhere: what reaches the CP is the
// per-profile fields of the rows the member ticks, through the same POST the add form uses.
// The pasted text is dropped as soon as it has been read (and on cancel), so the only copy
// left is the parsed, validated rows.
import { useMemo, useRef, useState } from "react";
import { Modal } from "../../../ui/Modal.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { parseAwsIni } from "../../../lib/awsConfigIni.ts";
import {
  MAX_BYTES,
  MAX_PROFILES,
  readAwsConfig,
  resolveImport,
  toImportSection,
  type ImportRow,
  type NativeConfig,
  type Reason,
  type RowEdit,
  safeKey,
} from "../../../lib/awsConfigImport.ts";
import { planSsmImport } from "../../../lib/settingsBundle.ts";
import { executeSsmPlan, loadSsmLists, type ExecRow } from "./ssmImportExec.ts";

type Tr = ReturnType<typeof useT>;

export function reasonText(tr: Tr, r: Reason): string {
  switch (r.code) {
    case "auth":
    case "sso_conflict":
    case "invalid":
      return tr(("ssm.imp_r_" + r.code) as MsgKey, { key: r.key || "?" });
    case "incomplete_sso":
      return tr("ssm.imp_r_incomplete_sso", { keys: r.missing.join(", ") });
    case "no_session":
      return tr("ssm.imp_r_no_session", { name: r.session || "?" });
    case "multi_hop":
    case "source_unsupported":
    case "source_unselected":
    case "source_missing":
      return tr(("ssm.imp_r_" + r.code) as MsgKey, { name: r.source || "?" });
    case "label_taken":
    case "label_dup":
    case "name_collision":
      return tr(("ssm.imp_r_" + r.code) as MsgKey, { name: r.other });
    default:
      return tr(("ssm.imp_r_" + r.code) as MsgKey);
  }
}

export function AwsConfigImport({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const tr = useT();
  const fileRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [cfg, setCfg] = useState<NativeConfig | null>(null);
  const [existing, setExisting] = useState<any[]>([]);
  const [edits, setEdits] = useState<Record<string, RowEdit>>({});
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<[number, number] | null>(null);
  const [results, setResults] = useState<ExecRow[] | null>(null);
  // Logins the last run implied, kept with its results: once the rows exist the preview counts
  // them as already registered, though none has been signed in yet.
  const [ranLogins, setRanLogins] = useState<{ logins: number; signins: number } | null>(null);
  const cancelRef = useRef(false);

  const preview = useMemo(() => (cfg ? resolveImport(cfg, existing, edits) : null), [cfg, existing, edits]);
  const creating = preview ? preview.rows.filter((r) => r.status === "create").length : 0;
  const ignored = preview ? preview.rows.reduce((n, r) => n + (r.reason ? 0 : r.ignored.length), 0) : 0;

  const edit = (name: string, e: RowEdit) => setEdits((all) => ({ ...all, [name]: { ...all[name], ...e } }));

  const read = async (source: string) => {
    setErr("");
    setResults(null);
    setRanLogins(null);
    // The raw text is not kept past this point, whatever the outcome.
    setText("");
    if (fileRef.current) fileRef.current.value = "";
    if (new TextEncoder().encode(source).byteLength > MAX_BYTES) {
      setErr(tr("ssm.imp_too_big"));
      return;
    }
    const parsed = parseAwsIni(source);
    if (!parsed.ok) {
      // A key name is file text too: only one this importer knows is shown.
      const key = safeKey(parsed.error.key ?? "") || "?";
      setErr(tr(("ssm.imp_err_" + parsed.error.code) as MsgKey, { line: parsed.error.line, key }));
      return;
    }
    const native = readAwsConfig(parsed.file);
    if (native.tooMany) {
      setErr(tr("ssm.imp_too_many", { max: MAX_PROFILES }));
      return;
    }
    const lists = await loadSsmLists();
    if (!lists) {
      setErr(tr("ssm.imp_unread"));
      return;
    }
    setExisting(lists.profiles);
    setEdits({});
    setCfg(native);
  };

  const apply = async () => {
    if (!preview) return;
    setBusy(true);
    cancelRef.current = false;
    try {
      // Re-read what exists now, so a retry after a partial run, or a change made in another
      // tab, is planned against the truth rather than the list the preview was built on.
      const lists = await loadSsmLists();
      if (!lists) {
        setErr(tr("ssm.imp_unread"));
        return;
      }
      const fresh = resolveImport(cfg!, lists.profiles, edits);
      const plan = planSsmImport(toImportSection(fresh), lists.profiles, []);
      setProgress([0, plan.profiles.length]);
      const rows = await executeSsmPlan(plan, lists.profiles, {
        cancelled: () => cancelRef.current,
        onProgress: (done, total) => setProgress([done, total]),
      });
      setResults(rows);
      const ssoLabels = new Set(plan.profiles.filter((x) => x.kind !== "assume_role").map((x) => x.label));
      const made = fresh.rows.filter((r) => r.status === "create" && r.kind === "sso" && ssoLabels.has(r.label));
      const ok = new Set(rows.filter((x) => x.outcome === "created").map((x) => x.label));
      const done = made.filter((r) => ok.has(r.label));
      setRanLogins({ logins: done.length, signins: new Set(done.map((r) => r.signIn)).size });
      onImported();
      const after = await loadSsmLists();
      if (after) setExisting(after.profiles);
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
    setCfg(null);
    onClose();
  };

  return (
    // No lockClose: it makes the whole panel inert, Stop included. close() cancels instead
    // while a run is in flight.
    <Modal title={tr("ssm.imp_title")} onClose={close} className="aws-import">
      <div className="ui-modal-body">
        <p className="field-help">{tr("ssm.imp_intro")}</p>
        {!cfg && (
          <div className="aws-import-input">
            <input
              ref={fileRef}
              type="file"
              className="cinput"
              aria-label={tr("ssm.imp_file")}
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (!f) return;
                if (f.size > MAX_BYTES) {
                  setErr(tr("ssm.imp_too_big"));
                  return;
                }
                void f.text().then(read);
              }}
            />
            <textarea
              className="cinput mono"
              rows={8}
              value={text}
              spellCheck={false}
              autoComplete="off"
              placeholder={tr("ssm.imp_paste")}
              aria-label={tr("ssm.imp_paste")}
              onChange={(e) => setText(e.target.value)}
            />
            <div className="flow">
              <button type="button" className="primary" disabled={!text.trim()} onClick={() => void read(text)}>
                {tr("ssm.imp_read")}
              </button>
            </div>
          </div>
        )}
        {err && (
          <p className="ssm-load-err" role="alert">
            {err}
          </p>
        )}
        {cfg && preview && (
          <>
            {cfg.secrets && (
              <p className="warn-text" role="alert">
                {tr("ssm.imp_secrets")}
              </p>
            )}
            {cfg.bomStripped && <p className="muted">{tr("ssm.imp_bom")}</p>}
            {preview.rows.length === 0 ? (
              <p className="muted">{tr("ssm.imp_none")}</p>
            ) : (
              <ul className="ssm-list aws-import-rows">
                {preview.rows.map((r) => (
                  <Row key={r.line} tr={tr} r={r} e={edits[r.name] || {}} preview={preview} edit={edit} busy={busy} />
                ))}
              </ul>
            )}
            <p className="field-help">
              {tr("ssm.imp_summary", { create: creating, logins: preview.logins, signins: preview.laptopSignIns })}
              {ignored > 0 && " " + tr("ssm.imp_ignored", { n: ignored })}
            </p>
            <p className="field-help">{tr("ssm.imp_after")}</p>
            {results && <Results tr={tr} rows={results} />}
            {results && ranLogins && (
              <p className="field-help">{tr("ssm.imp_res_logins", { logins: ranLogins.logins, signins: ranLogins.signins })}</p>
            )}
            <div className="flow">
              <button type="button" className="primary" disabled={busy || creating === 0} onClick={() => void apply()}>
                {progress ? tr("ssm.imp_running", { done: progress[0], total: progress[1] }) : tr("ssm.imp_do", { n: creating })}
              </button>
              <button type="button" className="ghost" onClick={busy ? () => (cancelRef.current = true) : () => setCfg(null)}>
                {busy ? tr("ssm.imp_stop") : tr("ssm.imp_other_file")}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function Row({
  tr,
  r,
  e,
  preview,
  edit,
  busy,
}: {
  tr: Tr;
  r: ImportRow;
  e: RowEdit;
  preview: NonNullable<ReturnType<typeof resolveImport>>;
  edit: (name: string, e: RowEdit) => void;
  busy: boolean;
}) {
  // A row the file itself rules out has nothing a member could change here.
  const editable = !r.reason || !(r.reason.code in FILE_REASONS);
  // The profile name, unless it is not safe to show (then the row goes by its line).
  const shown = r.display || tr("ssm.imp_unnamed", { line: r.line });
  const status =
    r.status === "create"
      ? tr(r.kind === "sso" ? "ssm.imp_s_create_sso" : "ssm.imp_s_create_role")
      : r.status === "exists"
        ? tr("ssm.imp_s_exists")
        : r.status === "unselected"
          ? tr("ssm.imp_s_unselected")
          : tr("ssm.imp_s_blocked", { reason: reasonText(tr, r.reason!) });
  return (
    <li className={"ssm-item aws-import-row " + r.status} data-row={r.display || "line-" + r.line}>
      <div className="ssm-item-head">
        <label className="aws-import-pick">
          <input
            type="checkbox"
            checked={r.status !== "exists" && editable && r.selected}
            disabled={busy || r.status === "exists" || !editable}
            aria-label={shown}
            onChange={(e) => edit(r.name, { selected: e.target.checked })}
          />
          <span className="ssm-alias">{shown}</span>
        </label>
        <span className="muted">{tr("ssm.imp_line", { line: r.line })}</span>
      </div>
      <div className="aws-import-status">{status}</div>
      {editable && r.status !== "exists" && (
        <div className="ssm-fgrid">
          <label className="ssm-fld">
            <span>{tr("ssm.imp_label")}</span>
            <input
              className="cinput"
              value={e.label ?? r.display}
              disabled={busy}
              onChange={(e) => edit(r.name, { label: e.target.value })}
            />
          </label>
          {r.kind === "assume_role" && (
            <label className="ssm-fld">
              <span>{tr("ssm.imp_duration")}</span>
              <input
                className="cinput"
                inputMode="numeric"
                placeholder="3600"
                value={e.duration ?? r.durationRaw ?? ""}
                disabled={busy}
                onChange={(e) => edit(r.name, { duration: e.target.value })}
              />
            </label>
          )}
          {r.kind === "assume_role" && (r.reason?.code === "source_missing" || r.source?.via === "picked") && (
            <label className="ssm-fld wide">
              <span>{tr("ssm.imp_pick_source", { name: r.sourceProfile ?? "" })}</span>
              <select
                className="cinput"
                value={e.sourceId ?? ""}
                disabled={busy}
                onChange={(e) => edit(r.name, { sourceId: e.target.value || undefined })}
              >
                <option value="">{tr("ssm.imp_pick_none")}</option>
                {preview.sourceChoices.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label} — {c.accountId} / {c.roleName} ({c.startUrl}, {c.ssoRegion})
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
      )}
      {(r.status === "create" || r.status === "exists") && (
        <div className="ssm-meta muted">
          {r.kind === "sso" && r.sso && <span>{tr("ssm.imp_meta_sso", { account: r.sso.accountId, role: r.sso.roleName })}</span>}
          {r.kind === "assume_role" && r.source && (
            <span>
              {tr(r.source.via === "picked" ? "ssm.imp_meta_role_picked" : "ssm.imp_meta_role", {
                arn: r.roleArn ?? "",
                source: r.source.label,
              })}
            </span>
          )}
          <span>
            {r.region ? tr("ssm.imp_region", { region: r.effectiveRegion }) : tr("ssm.imp_region_default", { region: r.effectiveRegion })}
          </span>
          {r.scopesChange && <span>{tr("ssm.imp_scopes")}</span>}
        </div>
      )}
    </li>
  );
}

// Reasons that come from the file itself; renaming or picking cannot fix them.
const FILE_REASONS: Record<string, true> = {
  keys: true,
  auth: true,
  mixed_auth: true,
  no_auth: true,
  no_session: true,
  sso_conflict: true,
  incomplete_sso: true,
  invalid: true,
  no_source_profile: true,
  self_source: true,
  multi_hop: true,
};

function Results({ tr, rows }: { tr: Tr; rows: ExecRow[] }) {
  return (
    <ul className="backup-result aws-import-results">
      {rows.map((r, i) => (
        <li key={i} className={r.outcome}>
          {tr(("ssm.imp_o_" + r.outcome) as MsgKey, { label: r.label, code: r.code ?? "" })}
        </li>
      ))}
    </ul>
  );
}
