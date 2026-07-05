import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import { useApp } from "../state.jsx";
import { useToast } from "./ToastProvider.jsx";
import { apiJSON, chatCreate, assistantList, errText } from "../api.js";
import { displayName, stateInfo } from "../lib/sessionview.js";
import { agentOf } from "../agents/registry.ts";
import { baseName, langFor } from "../lib/filemeta.js";
import type { Session } from "../types/session.ts";
import type { Assistant } from "../types/assistant.ts";

// Remember the session the user last sent to, so it's re-selected next time (if still
// 入力待ち). A single key across files — "the session I'm feeding excerpts to".
const LAST_SESSION_KEY = "af.sendsel.lastSession";

// SendSelectionModal quotes a selected range of a code/source file and sends it — with
// the file path, line range, and a comment — either directly to a running session (the
// coding agent that will act on it) or to an assistant chat (docs/19 "引用してセッションへ").
// Session = a direct send_to_session; assistant = open a chat prefilled with the quote.
interface SendSelectionModalProps {
  filePath: string;
  quote: string;
  startLine: number;
  endLine: number;
  onClose: () => void;
}

export default function SendSelectionModal({ filePath, quote, startLine, endLine, onClose }: SendSelectionModalProps) {
  const { sessions, openChat } = useApp();
  const toast = useToast();
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [comment, setComment] = useState("");
  const [target, setTarget] = useState(""); // "session:<name>" | "assistant:<id>"
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, []);

  // The repo the file lives in (repos/<repo>/…), used to prefer a session in it.
  const fileRepo = filePath.startsWith("repos/") ? filePath.split("/")[1] : null;
  // "入力待ち": an alive coding-agent session that's idle (not working/question/…) — the
  // best target since it picks the instruction up immediately (see sessionview stateInfo).
  const isWaiting = (s: Session) =>
    !!s.alive && agentOf(s.kind).caps.chat && (!s.state || s.state === "idle");
  // Rank alive sessions: same repo first, then 入力待ち. Used for both the default
  // selection and the option order.
  const rank = (s: Session) => (fileRepo && s.repo === fileRepo ? 2 : 0) + (isWaiting(s) ? 1 : 0);
  const sortedSessions = useMemo(
    () => sessions.slice().sort((a, b) => rank(b) - rank(a)),
    [sessions, fileRepo],
  );

  // Default target: the last session sent to if it's still 入力待ち; else the highest-ranked
  // alive session (same-repo / 入力待ち優先); else the first session, else the first assistant.
  useEffect(() => {
    if (target) return;
    const last = localStorage.getItem(LAST_SESSION_KEY);
    const lastS = last ? sessions.find((s) => s.name === last) : undefined;
    if (lastS && isWaiting(lastS)) {
      setTarget(`session:${lastS.name}`);
      return;
    }
    const best = sortedSessions.find((s) => s.alive) || sortedSessions[0];
    if (best) setTarget(`session:${best.name}`);
    else if (assistants[0]) setTarget(`assistant:${assistants[0].id}`);
  }, [sortedSessions, sessions, assistants, target]);

  // The currently-selected session (if the target is a session), for the stopped guard.
  const selectedSession =
    target.startsWith("session:") ? sessions.find((s) => s.name === target.slice("session:".length)) : undefined;
  const sessionStopped = !!selectedSession && !selectedSession.alive;

  const loc = startLine === endLine ? `L${startLine}` : `L${startLine}–L${endLine}`;
  const composed = useMemo(() => {
    const fence = "```";
    const body = `ファイル \`${filePath}\` の ${loc}:\n\n${fence}${langFor(filePath)}\n${quote}\n${fence}`;
    return comment.trim() ? `${body}\n\n${comment.trim()}` : body;
  }, [filePath, loc, quote, comment]);

  const isAssistant = target.startsWith("assistant:");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!target || busy || sessionStopped) return; // can't send to a stopped session
    setBusy(true);
    try {
      if (isAssistant) {
        const id = target.slice("assistant:".length);
        // Open a chat with the file attached (context) and the quote prefilled — the user
        // reviews and sends (consistent with the other assistant-open flows).
        const c = await chatCreate(id, `${baseName(filePath)} 引用`, { attachPath: filePath });
        if (c && c.id) {
          openChat(c.id, composed);
          onClose();
        } else toast("チャットの作成に失敗しました");
      } else {
        const name = target.slice("session:".length);
        // apiJSON resolves (doesn't throw) on a non-2xx error body, so check res.error.
        const res = await apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", { prompt: composed });
        if (res && res.error) {
          toast(errText(res.error) || `${name} への送信に失敗しました`);
          return;
        }
        localStorage.setItem(LAST_SESSION_KEY, name); // remember for next time
        toast(`${name} に送信しました`, { kind: "success" });
        onClose();
      }
    } catch {
      toast("送信に失敗しました");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="選択範囲をセッション/アシスタントに送る" onClose={onClose} className="session-modal" as="form" onSubmit={submit} lockClose={busy}>
      <div className="modal-body">
        <div className="field">
          <div className="field-label">送信先</div>
          <label className="pick-field">
            <select
              value={target}
              onChange={(e) => {
                setTarget(e.target.value);
                if (e.target.value.startsWith("session:"))
                  localStorage.setItem(LAST_SESSION_KEY, e.target.value.slice("session:".length));
              }}
            >
              {sortedSessions.length > 0 && (
                <optgroup label="セッション（直接送信）">
                  {sortedSessions.map((s) => (
                    <option key={s.name} value={`session:${s.name}`}>
                      {displayName(s)}（{s.name}・{stateInfo(s).text}
                      {fileRepo && s.repo === fileRepo ? "・同レポ" : ""}）
                    </option>
                  ))}
                </optgroup>
              )}
              {assistants.length > 0 && (
                <optgroup label="アシスタント（チャットで開く）">
                  {assistants.map((a) => (
                    <option key={a.id} value={`assistant:${a.id}`}>
                      {a.name}
                    </option>
                  ))}
                </optgroup>
              )}
            </select>
          </label>
          <div className={"field-help" + (sessionStopped ? " field-warn" : "")}>
            {sessionStopped
              ? "⚠ このセッションは停止中です。送信できません（先に起動するか、別の送信先を選んでください）。"
              : isAssistant
                ? "このアシスタントとのチャットを開き、引用を下書きします（送信は会話側で）。"
                : "選択したセッションに引用＋コメントを直接送信します。"}
          </div>
        </div>

        <div className="field">
          <div className="field-label">コメント（指示）</div>
          <label className="pick-field">
            <textarea
              className="assistant-persona"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              onKeyDown={(e) => {
                // Ctrl/⌘+Enter submits (Enter alone keeps making newlines in the comment).
                if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                  e.preventDefault();
                  e.currentTarget.form?.requestSubmit();
                }
              }}
              rows={3}
              placeholder="例: この場面、テンポを上げて。/ この関数の境界条件を直して。（Ctrl+Enter で送信）"
              autoFocus
            />
          </label>
        </div>

        <div className="field">
          <div className="field-label">プレビュー（{loc}）</div>
          <pre className="send-preview">{composed}</pre>
        </div>
      </div>

      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose}>
          キャンセル
        </button>
        <button type="submit" className="primary" disabled={!target || busy || sessionStopped}>
          {isAssistant ? "アシスタントで開く" : "セッションに送信"}
        </button>
      </footer>
    </Modal>
  );
}
