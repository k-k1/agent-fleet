import { useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { apiJSON, errText } from "../api.js";
import { useToast } from "./ToastProvider.jsx";

// SessionTitleModal: manual title edit for an existing session (right-click →
// タイトルを変更), with an "AIに提案してもらう" button that fills the field via the
// preview-only /title/suggest endpoint — distinct from /title/regenerate, which
// drives the auto-suggestion banner and only works while the title is still empty.
interface SessionTitleModalProps {
  name: string;
  title: string;
  onClose: () => void;
  onSaved: () => void;
}

export default function SessionTitleModal({ name, title, onClose, onSaved }: SessionTitleModalProps) {
  const [value, setValue] = useState(title);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const toast = useToast();
  const busy = saving || suggesting;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setSaving(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/title/set`, "POST", {
        title: value.trim(),
      });
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      onSaved();
      onClose();
    } catch {
      toast("保存に失敗しました（通信エラー）");
    } finally {
      setSaving(false);
    }
  };

  const suggest = async () => {
    if (busy) return;
    setSuggesting(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/title/suggest`, "POST");
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      if (typeof j.suggestedTitle === "string") setValue(j.suggestedTitle);
    } catch {
      toast("提案の取得に失敗しました（通信エラー）");
    } finally {
      setSuggesting(false);
    }
  };

  return (
    <Modal title="タイトルを変更" onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="modal-body">
        <div className="field">
          <div className="field-label">タイトル</div>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="未入力にすると自動命名に戻ります"
            maxLength={80}
            autoFocus
          />
        </div>
        <button type="button" className="ghost" onClick={suggest} disabled={busy}>
          <Icon name={suggesting ? "loading" : "lightbulb"} spin={suggesting} /> AIに提案してもらう
        </button>
      </div>
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose} disabled={saving}>
          キャンセル
        </button>
        <button type="submit" className="primary" disabled={saving}>
          保存
        </button>
      </footer>
    </Modal>
  );
}
