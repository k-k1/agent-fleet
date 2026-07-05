import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import { useApp } from "../state.jsx";
import { useToast } from "./ToastProvider.jsx";
import { apiJSON, chatCreate, assistantList } from "../api.js";
import { displayName } from "../lib/sessionview.js";
import { baseName, langFor } from "../lib/filemeta.js";
import type { Assistant } from "../types/assistant.ts";

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

  // Default target: a running session if any, else the first assistant.
  useEffect(() => {
    if (target) return;
    const live = sessions.find((s) => s.alive);
    if (live) setTarget(`session:${live.name}`);
    else if (sessions[0]) setTarget(`session:${sessions[0].name}`);
    else if (assistants[0]) setTarget(`assistant:${assistants[0].id}`);
  }, [sessions, assistants, target]);

  const loc = startLine === endLine ? `L${startLine}` : `L${startLine}–L${endLine}`;
  const composed = useMemo(() => {
    const fence = "```";
    const body = `ファイル \`${filePath}\` の ${loc}:\n\n${fence}${langFor(filePath)}\n${quote}\n${fence}`;
    return comment.trim() ? `${body}\n\n${comment.trim()}` : body;
  }, [filePath, loc, quote, comment]);

  const isAssistant = target.startsWith("assistant:");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!target || busy) return;
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
        await apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", { prompt: composed });
        toast(`${name} に送信しました`);
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
            <select value={target} onChange={(e) => setTarget(e.target.value)}>
              {sessions.length > 0 && (
                <optgroup label="セッション（直接送信）">
                  {sessions.map((s) => (
                    <option key={s.name} value={`session:${s.name}`}>
                      {displayName(s)}（{s.name}{s.alive ? "" : "・停止中"}）
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
          <div className="field-help">
            {isAssistant
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
              rows={3}
              placeholder="例: この場面、テンポを上げて。/ この関数の境界条件を直して。"
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
        <button type="submit" className="primary" disabled={!target || busy}>
          {isAssistant ? "アシスタントで開く" : "セッションに送信"}
        </button>
      </footer>
    </Modal>
  );
}
