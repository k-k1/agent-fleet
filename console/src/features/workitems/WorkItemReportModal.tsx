// Posting a report on the work back as a ticket comment (docs/log/80 §80.10 / ADR 0061
// decision 6).
//
// The only write af makes against a tracker, and it is gated on a human reading the
// draft. The modal therefore shows, in this order: WHERE it will be posted, WHAT will be
// posted (editable), and only then the post button. Nothing here fires on mount.
//
// The draft's narrative line is left blank on purpose — af fills in branch and files
// (facts it alone has per session) and lets the user say what happened. A generated
// summary would read plausibly enough to be posted unread onto someone else's ticket.
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, errText } from "../../core/api/client.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import type { SessionFile } from "../mirror/sessionFiles.ts";
import { workItemComment } from "./api.ts";
import { composeReportDraft, reportTarget } from "./report.ts";
import type { WorkItem, WorkItemSessionRef } from "./read.ts";

interface Props {
  item: WorkItem;
  sessions: WorkItemSessionRef[];
  onClose(): void;
}

export function WorkItemReportModal({ item, sessions, onClose }: Props) {
  const tr = useT();
  const toast = useToast();
  const [sessionName, setSessionName] = useState(sessions[0]?.sessionName || "");
  const [files, setFiles] = useState<SessionFile[]>([]);
  const [loadingFiles, setLoadingFiles] = useState(true);
  const [note, setNote] = useState("");
  const [body, setBody] = useState("");
  const [edited, setEdited] = useState(false);
  const [busy, setBusy] = useState(false);

  const session = useMemo(
    () => sessions.find((s) => s.sessionName === sessionName) || sessions[0],
    [sessions, sessionName],
  );

  // The changed files come from the same source as mirror: the Agent's all-time aggregation of
  // the transcript (docs/log/68). The draft can still be built if the fetch fails, and to avoid
  // confusing "no files" with "could not fetch" the draft only ever says there are no changed
  // files.
  useEffect(() => {
    let alive = true;
    setLoadingFiles(true);
    void api(`api/sessions/${encodeURIComponent(sessionName)}/messages`)
      .then((d: unknown) => {
        if (!alive) return;
        const arr = (d as { files?: SessionFile[] })?.files;
        setFiles(Array.isArray(arr) ? arr : []);
      })
      .catch(() => {})
      .finally(() => alive && setLoadingFiles(false));
    return () => {
      alive = false;
    };
  }, [sessionName]);

  // Rebuild the draft only while it is untouched, so switching session after editing never
  // discards what the user wrote.
  useEffect(() => {
    if (edited || !session) return;
    setBody(composeReportDraft({ item, session, files, note }));
  }, [item, session, files, note, edited]);

  const post = async () => {
    if (!body.trim() || busy) return;
    setBusy(true);
    try {
      const res = (await workItemComment({ provider: item.provider, key: item.key, body: body.trim() })) as {
        error?: unknown;
        url?: string;
      };
      if (res?.error) {
        toast(errText(res.error) || t("wi.report_failed"), { kind: "warn", duration: 8000 });
        return;
      }
      toast(t("wi.report_posted"), { kind: "success" });
      onClose();
    } catch {
      toast(t("wi.report_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("wi.report_title")} onClose={onClose} className="wi-rmodal">
      {/* Content must sit in ui-modal-body / ui-modal-foot. ui-modal itself has no padding (the
          heading and footer carry their own), so a child placed directly in it sticks to the
          frame. */}
      <div className="ui-modal-body">
        {/* The destination comes first: nobody should have to work out where it went after
            pressing post. */}
        <div className="wi-rtarget">
          <span className="wi-rlabel">{tr("wi.report_to")}</span>
          <a href={item.url} target="_blank" rel="noreferrer noopener">
            {reportTarget(item)}
          </a>
        </div>
        {sessions.length > 1 && (
          <label className="wi-rsession">
            <span>{tr("wi.report_session")}</span>
            <select value={sessionName} onChange={(e) => setSessionName(e.target.value)}>
              {sessions.map((s) => (
                <option key={s.id} value={s.sessionName}>
                  {s.sessionName}
                  {s.branch ? ` (${s.branch})` : ""}
                </option>
              ))}
            </select>
          </label>
        )}
        {!edited && (
          <label className="wi-rnote">
            <span>{tr("wi.report_note")}</span>
            <input value={note} onChange={(e) => setNote(e.target.value)} placeholder={tr("wi.report_note_ph")} />
          </label>
        )}
        <label className="wi-rbody">
          <span>{tr("wi.report_body")}</span>
          <textarea
            rows={12}
            value={body}
            spellCheck={false}
            onChange={(e) => {
              setBody(e.target.value);
              setEdited(true);
            }}
          />
        </label>
        <p className="wi-rhint">{loadingFiles ? tr("wi.report_loading_files") : tr("wi.report_hint")}</p>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        <Button onClick={() => void post()} disabled={busy || !body.trim()}>
          {busy ? tr("wi.report_posting") : tr("wi.report_post")}
        </Button>
      </footer>
    </Modal>
  );
}
