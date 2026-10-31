// AwsConfigExport — "Export as ~/.aws/config" on the AWS profiles/SSM tab (issue #3017, part 3).
//
// The file is built in the browser from GET api/ssm/profiles by awsConfigExport.ts (a validated
// renderer) and goes to the member's disk only; nothing is sent anywhere. It is a fragment to
// merge by hand, and the dialog and the file name say so.
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../../ui/Modal.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { api } from "../../../core/api/client.ts";
import { EXPORT_FILENAME, renderAwsConfig, type ExportReason } from "../../../lib/awsConfigExport.ts";

type Tr = ReturnType<typeof useT>;

export function exportReasonText(tr: Tr, r: ExportReason): string {
  switch (r.code) {
    case "name_collision":
      return tr("ssm.exp_r_name_collision", { name: r.other });
    case "incomplete":
      return tr("ssm.exp_r_incomplete", { fields: r.missing.join(", ") });
    case "invalid":
      return tr("ssm.exp_r_invalid", { field: r.field });
    case "source_excluded":
      return tr("ssm.exp_r_source_excluded", { name: r.source });
    case "session_conflict":
      return tr("ssm.exp_r_session_conflict", { session: r.session });
    default:
      return tr(("ssm.exp_r_" + r.code) as MsgKey);
  }
}

function download(text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = EXPORT_FILENAME;
  document.body.append(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function AwsConfigExport({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    api("api/ssm/profiles")
      .then((r) => {
        if (!live) return;
        if (Array.isArray(r)) setRows(r);
        else setErr(tr("ssm.exp_unread"));
      })
      .catch(() => live && setErr(tr("ssm.exp_unread")));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const out = useMemo(() => {
    if (!rows) return null;
    try {
      return renderAwsConfig(rows);
    } catch {
      return null;
    }
  }, [rows]);

  return (
    <Modal title={tr("ssm.exp_title")} onClose={onClose} className="aws-import">
      <div className="ui-modal-body">
        <p className="field-help">{tr("ssm.exp_intro")}</p>
        <p className="warn-text" role="note">
          {tr("ssm.exp_not_replacement")}
        </p>
        {err && (
          <p className="ssm-load-err" role="alert">
            {err}
          </p>
        )}
        {rows && !out && (
          <p className="ssm-load-err" role="alert">
            {tr("ssm.exp_failed")}
          </p>
        )}
        {out && (
          <>
            <p className="field-help">{tr("ssm.exp_summary", { n: out.included.length, skipped: out.excluded.length })}</p>
            {out.excluded.length > 0 && (
              <ul className="ssm-list aws-import-rows" aria-label={tr("ssm.exp_left_out")}>
                {out.excluded.map((e, i) => (
                  <li key={i} className="ssm-item aws-import-row blocked">
                    <span className="ssm-alias">{e.label}</span>
                    <div className="aws-import-status">{tr("ssm.exp_s_left_out", { reason: exportReasonText(tr, e.reason) })}</div>
                  </li>
                ))}
              </ul>
            )}
            <pre className="mono aws-export-text" aria-label={tr("ssm.exp_preview")}>
              {out.text}
            </pre>
            <div className="flow">
              <button type="button" className="primary" disabled={out.included.length === 0} onClick={() => download(out.text)}>
                {tr("ssm.exp_download", { file: EXPORT_FILENAME })}
              </button>
              <button type="button" className="ghost" onClick={onClose}>
                {tr("ssm.exp_close")}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
