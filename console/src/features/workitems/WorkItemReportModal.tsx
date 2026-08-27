// 「作業の報告をコメントする」 (docs/80 §80.10 / ADR 0061 決定 6).
//
// ★ The only write af makes against a tracker, and it is gated on a human reading the
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

  // 変更ファイルはミラーと同じ源（転写を Agent 側で全期間集計したもの・docs/68）。
  // 取れなくても下書きは作れる —— ファイル一覧が空なのと「取れなかった」のを混同しない
  // よう、下書き側は「変更ファイルなし」とだけ言う。
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

  // 下書きは「まだ手を入れていない間だけ」作り直す。編集後にセッションを切り替えても
  // 書いた文章を消さない。
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
      {/* ★ 中身は ui-modal-body / ui-modal-foot に載せる。ui-modal 自身に padding は
          無く（見出しと footer が自分で持つ形）、直に子を置くと本文だけが枠に貼りつく。 */}
      <div className="ui-modal-body">
        {/* 宛先が最初。押してから「どこに出たのか」を考える形にしない。 */}
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
