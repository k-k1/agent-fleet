import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import { useApp } from "../state.jsx";
import { useToast } from "./ToastProvider.jsx";
import { apiJSON, chatCreate, assistantList, errText } from "../api.js";
import { displayName, stateInfo } from "../lib/sessionview.js";
import { sessionPanes, ordClass } from "../lib/panebadge.js";
import { agentOf } from "../agents/registry.ts";
import { baseName, langFor } from "../lib/filemeta.js";
import type { Session } from "../types/session.ts";
import type { Assistant } from "../types/assistant.ts";

// Remember the session the user last sent to, so it's re-selected next time (if still
// 入力待ち). A single key — "the session I'm feeding excerpts/files to".
const LAST_SESSION_KEY = "af.sendsel.lastSession";

// SendSelectionModal sends a file — with a comment — to a running session (the coding
// agent that acts on it) or an assistant chat. Two modes:
//   - QUOTE mode (quote/startLine/endLine given): a selected range is quoted inline (a
//     Files/CodeView selection → session/assistant, docs/19 Phase C).
//   - FILE mode (no quote): the WHOLE file is handed by PATH reference to a SESSION — for
//     work that produces a file (e.g. "translate this manual and save it"), which the
//     chat assistant can't do (it's chat-only, no file writes). Assistants are hidden here
//     since attaching a file to a chat is the Files "アシスタントで開く" flow.
interface SendSelectionModalProps {
  filePath: string;
  quote?: string;
  startLine?: number;
  endLine?: number;
  onClose: () => void;
}

export default function SendSelectionModal({ filePath, quote, startLine, endLine, onClose }: SendSelectionModalProps) {
  const { sessions, openChat, layout } = useApp();
  const toast = useToast();
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [comment, setComment] = useState("");
  const [target, setTarget] = useState(""); // "session:<name>" | "assistant:<id>"
  const [busy, setBusy] = useState(false);

  const fileMode = quote == null; // whole file (session-only) vs an inline quote

  useEffect(() => {
    if (fileMode) return; // no assistant targets in file mode
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, [fileMode]);

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
  // alive session (same-repo / 入力待ち優先); else the first session, else (quote mode) an assistant.
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
    else if (!fileMode && assistants[0]) setTarget(`assistant:${assistants[0].id}`);
  }, [sortedSessions, sessions, assistants, target, fileMode]);

  // The currently-selected session (if the target is a session), for the stopped guard.
  const selectedSession =
    target.startsWith("session:") ? sessions.find((s) => s.name === target.slice("session:".length)) : undefined;
  const sessionStopped = !!selectedSession && !selectedSession.alive;

  // File-mode references the file by an absolute-ish path (browse root is home) so a
  // session resolves it regardless of its own working directory.
  const pathRef = "~/" + filePath;
  const loc = fileMode ? baseName(filePath) : startLine === endLine ? `L${startLine}` : `L${startLine}–L${endLine}`;
  const composed = useMemo(() => {
    if (fileMode) {
      const c = comment.trim();
      return (c ? c + "\n\n" : "") + `対象ファイル: ${pathRef}`;
    }
    const fence = "```";
    const body = `ファイル \`${filePath}\` の ${loc}:\n\n${fence}${langFor(filePath)}\n${quote}\n${fence}`;
    return comment.trim() ? `${body}\n\n${comment.trim()}` : body;
  }, [fileMode, pathRef, filePath, loc, quote, comment]);

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
        // Toast with the session's display name, plus color-matched pane-number badges if
        // it's open in the main-area panes (same ordinals as the Sessions list).
        const sent = sessions.find((x) => x.name === name);
        const opens = sessionPanes(layout).get(name) || [];
        toast(
          <span className="toast-sent">
            {opens.map((o) => (
              <span key={o.id} className={"toast-ord " + ordClass(o.ordinal)}>
                {o.ordinal}
              </span>
            ))}
            {(sent ? displayName(sent) : name) + " に送信しました"}
          </span>,
          { kind: "success" },
        );
        onClose();
      }
    } catch {
      toast("送信に失敗しました");
    } finally {
      setBusy(false);
    }
  };

  const noTarget = sortedSessions.length === 0 && (fileMode || assistants.length === 0);

  return (
    <Modal
      title={fileMode ? "ファイルをセッションに送る" : "選択範囲をセッション/アシスタントに送る"}
      onClose={onClose}
      className="session-modal"
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="modal-body">
        <div className="field">
          <div className="field-label">送信先</div>
          {noTarget ? (
            <div className="field-help field-warn">
              ⚠ 稼働中のセッションがありません。セッションを起動してから送ってください。
            </div>
          ) : (
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
                {!fileMode && assistants.length > 0 && (
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
          )}
          <div className={"field-help" + (sessionStopped ? " field-warn" : "")}>
            {sessionStopped
              ? "⚠ このセッションは停止中です。送信できません（先に起動するか、別の送信先を選んでください）。"
              : fileMode
                ? "ファイルはパスで渡します（セッションが自分で読み取り・書き込みします）。大きなファイルの翻訳など、ファイル出力を伴う作業向けです。"
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
              placeholder={
                fileMode
                  ? "例: 日本語に翻訳して <名前>.ja.md に保存して。長いので分割しながら進めて。（Ctrl+Enter で送信）"
                  : "例: この場面、テンポを上げて。/ この関数の境界条件を直して。（Ctrl+Enter で送信）"
              }
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
