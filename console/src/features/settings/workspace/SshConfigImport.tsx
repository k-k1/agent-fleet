// SshConfigImport — "Import from ~/.ssh/config" on the AWS profiles/SSM tab (issue #3017, part 1).
//
// Same promises as the ~/.aws/config import: the file is read in the browser and never sent; what
// reaches the CP is the alias / profile / instance id / region of the rows the member ticks,
// through the same POST the add-host form uses. Nothing of a ProxyCommand is kept past the
// reader (sshConfigImport.ts), so there is nothing here to display or send. The pasted text is
// dropped as soon as it has been read, and on cancel.
import { useMemo, useRef, useState } from "react";
import { Modal } from "../../../ui/Modal.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { MAX_BYTES } from "../../../lib/awsConfigImport.ts";
import { parseSshConfig } from "../../../lib/sshConfigIni.ts";
import {
  MAX_ENTRIES,
  readSshConfig,
  resolveSshImport,
  toSshImportSection,
  type NativeSsh,
  type SshEdit,
  type SshReason,
  type SshRow,
  type SshPreview,
} from "../../../lib/sshConfigImport.ts";
import { planSsmImport } from "../../../lib/settingsBundle.ts";
import { executeSsmPlan, loadSsmLists, type ExecRow } from "./ssmImportExec.ts";

type Tr = ReturnType<typeof useT>;

export function sshReasonText(tr: Tr, r: SshReason): string {
  switch (r.code) {
    case "invalid":
    case "inherited":
      return tr(("ssm.ssh_r_" + r.code) as MsgKey, { key: r.key });
    case "profile_missing":
    case "profile_ambiguous":
      return tr(("ssm.ssh_r_" + r.code) as MsgKey, { name: r.name });
    case "alias_taken":
    case "alias_dup":
      return tr(("ssm.ssh_r_" + r.code) as MsgKey, { name: r.other });
    default:
      return tr(("ssm.ssh_r_" + r.code) as MsgKey);
  }
}

// Reasons the file itself gives; an alias edit or a profile pick cannot fix them.
const RESOLVER_REASONS = new Set(["alias_empty", "alias_invalid", "alias_taken", "alias_dup", "profile_unset", "profile_missing", "profile_ambiguous", "profile_label_dup"]);

export function SshConfigImport({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const tr = useT();
  const fileRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [cfg, setCfg] = useState<NativeSsh | null>(null);
  const [lists, setLists] = useState<{ profiles: any[]; hosts: any[] }>({ profiles: [], hosts: [] });
  const [edits, setEdits] = useState<Record<string, SshEdit>>({});
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<[number, number] | null>(null);
  const [results, setResults] = useState<ExecRow[] | null>(null);
  const cancelRef = useRef(false);

  const preview = useMemo(() => (cfg ? resolveSshImport(cfg, lists.profiles, lists.hosts, edits) : null), [cfg, lists, edits]);
  const creating = preview ? preview.rows.filter((r) => r.status === "create").length : 0;
  const edit = (id: string, e: SshEdit) => setEdits((all) => ({ ...all, [id]: { ...all[id], ...e } }));

  const read = async (source: string) => {
    setErr("");
    setResults(null);
    // The raw text is not kept past this point, whatever the outcome.
    setText("");
    if (fileRef.current) fileRef.current.value = "";
    if (new TextEncoder().encode(source).byteLength > MAX_BYTES) {
      setErr(tr("ssm.imp_too_big"));
      return;
    }
    const parsed = parseSshConfig(source);
    if (!parsed.ok) {
      setErr(tr(("ssm.ssh_err_" + parsed.error.code) as MsgKey, { line: parsed.error.line }));
      return;
    }
    const native = readSshConfig(parsed.file);
    if (native.tooMany) {
      setErr(tr("ssm.ssh_too_many", { max: MAX_ENTRIES }));
      return;
    }
    const cur = await loadSsmLists();
    if (!cur) {
      setErr(tr("ssm.imp_unread"));
      return;
    }
    setLists(cur);
    setEdits({});
    setCfg(native);
  };

  const apply = async () => {
    if (!cfg) return;
    setBusy(true);
    cancelRef.current = false;
    try {
      // Re-read what exists now, so a retry after a partial run is planned against the truth.
      const cur = await loadSsmLists();
      if (!cur) {
        setErr(tr("ssm.imp_unread"));
        return;
      }
      const fresh = resolveSshImport(cfg, cur.profiles, cur.hosts, edits);
      const plan = planSsmImport(toSshImportSection(fresh), cur.profiles, cur.hosts);
      setProgress([0, plan.hosts.length]);
      const rows = await executeSsmPlan(plan, cur.profiles, {
        cancelled: () => cancelRef.current,
        onProgress: (done, total) => setProgress([done, total]),
      });
      setResults(rows);
      onImported();
      const after = await loadSsmLists();
      if (after) setLists(after);
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
    <Modal title={tr("ssm.ssh_title")} onClose={close} className="aws-import">
      <div className="ui-modal-body">
        <p className="field-help">{tr("ssm.ssh_intro")}</p>
        {!cfg && (
          <div className="aws-import-input">
            <input
              ref={fileRef}
              type="file"
              className="cinput"
              aria-label={tr("ssm.ssh_file")}
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
              placeholder={tr("ssm.ssh_paste")}
              aria-label={tr("ssm.ssh_paste")}
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
                {tr("ssm.ssh_secrets")}
              </p>
            )}
            {cfg.bomStripped && <p className="muted">{tr("ssm.ssh_bom")}</p>}
            {preview.rows.length === 0 ? (
              <p className="muted">{tr("ssm.ssh_none")}</p>
            ) : (
              <ul className="ssm-list aws-import-rows">
                {preview.rows.map((r) => (
                  <Row key={r.id} tr={tr} r={r} e={edits[r.id] || {}} preview={preview} edit={edit} busy={busy} />
                ))}
              </ul>
            )}
            <p className="field-help">{tr("ssm.ssh_summary", { create: creating })}</p>
            {creating > 0 && (
              <p className="warn-text" role="note">
                {tr("ssm.ssh_method_changes")}
              </p>
            )}
            <p className="field-help">{tr("ssm.ssh_after")}</p>
            {results && (
              <ul className="backup-result aws-import-results">
                {results.map((r, i) => (
                  <li key={i} className={r.outcome}>
                    {tr(("ssm.imp_o_" + r.outcome) as MsgKey, { label: r.label, code: r.code ?? "" })}
                  </li>
                ))}
              </ul>
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
  r: SshRow;
  e: SshEdit;
  preview: SshPreview;
  edit: (id: string, e: SshEdit) => void;
  busy: boolean;
}) {
  const isHost = r.kind === "host";
  // A host the file rules out has nothing a member could change here; one the resolver blocked may be fixable.
  const editable = isHost && (!r.reason || RESOLVER_REASONS.has(r.reason.code));
  const shown = r.display || (isHost ? tr("ssm.ssh_unnamed", { line: r.line }) : tr(("ssm.ssh_kind_" + r.kind) as MsgKey, { line: r.line }));
  const status =
    r.status === "create"
      ? tr("ssm.ssh_s_create")
      : r.status === "exists"
        ? tr("ssm.imp_s_exists")
        : r.status === "unselected"
          ? tr("ssm.imp_s_unselected")
          : tr("ssm.imp_s_blocked", { reason: sshReasonText(tr, r.reason!) });
  const needsProfile = r.reason?.code === "profile_unset" || r.reason?.code === "profile_missing" || r.via === "picked";
  return (
    <li className={"ssm-item aws-import-row " + r.status} data-row={r.display || "line-" + r.line}>
      <div className="ssm-item-head">
        <label className="aws-import-pick">
          <input
            type="checkbox"
            checked={r.status !== "exists" && editable && r.selected}
            disabled={busy || r.status === "exists" || !editable}
            aria-label={shown}
            onChange={(ev) => edit(r.id, { selected: ev.target.checked })}
          />
          <span className="ssm-alias">{shown}</span>
        </label>
        <span className="muted">{tr("ssm.imp_line", { line: r.line })}</span>
      </div>
      <div className="aws-import-status">{status}</div>
      {editable && r.status !== "exists" && (
        <div className="ssm-fgrid">
          <label className="ssm-fld">
            <span>{tr("ssm.ssh_alias")}</span>
            <input className="cinput" value={e.alias ?? r.display} disabled={busy} onChange={(ev) => edit(r.id, { alias: ev.target.value })} />
          </label>
          {needsProfile && (
            <label className="ssm-fld wide">
              <span>{tr("ssm.ssh_pick_profile")}</span>
              <select className="cinput" value={e.profileId ?? ""} disabled={busy} onChange={(ev) => edit(r.id, { profileId: ev.target.value || undefined })}>
                <option value="">{tr("ssm.imp_pick_none")}</option>
                {preview.profileChoices.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.label} ({c.awsName})
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
      )}
      {(r.status === "create" || r.status === "exists") && (
        <div className="ssm-meta muted">
          <span>{tr("ssm.ssh_meta", { instance: r.instanceId, profile: r.profileLabel ?? "" })}</span>
          {r.region && <span>{tr("ssm.imp_region", { region: r.region })}</span>}
          {r.ignored.length > 0 && <span>{tr("ssm.ssh_ignored", { keys: r.ignored.join(", ") })}</span>}
        </div>
      )}
    </li>
  );
}
