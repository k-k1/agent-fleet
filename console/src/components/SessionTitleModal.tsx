import { useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { apiJSON, errText } from "../api.js";
import { useToast } from "./ToastProvider.jsx";

// SessionTitleModal: manual title edit for an existing session (right-click →
// タイトルを変更). The "AIに提案してもらう" button fetches a candidate via the
// preview-only /title/suggest endpoint and shows it as a proposal the user can
// apply — it never overwrites what they've typed without an explicit click.
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
  const [proposal, setProposal] = useState(""); // AI candidate awaiting the user's OK
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
      if (typeof j.suggestedTitle === "string" && j.suggestedTitle) {
        // Show it as a proposal — do NOT clobber the field the user may be editing.
        setProposal(j.suggestedTitle);
      }
    } catch {
      toast("提案の取得に失敗しました（通信エラー）");
    } finally {
      setSuggesting(false);
    }
  };

  const applyProposal = () => {
    setValue(proposal);
    setProposal("");
  };

  return (
    <Modal
      title="タイトルを変更"
      onClose={onClose}
      className="session-modal title-modal"
      as="form"
      onSubmit={submit}
      lockClose={saving}
    >
      <div className="modal-body">
        <div className="field">
          <div className="field-label">タイトル</div>
          <input
            type="text"
            className="ti-input"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder="例: セッションタイトルの自動提案"
            maxLength={80}
            autoFocus
          />
          <div className="field-help">未入力のまま保存すると自動命名（リポジトリ名＋日時）に戻ります。</div>
        </div>

        <div className="ti-suggest-row">
          <button type="button" className="ti-suggest-btn" onClick={suggest} disabled={busy}>
            <Icon name={suggesting ? "loading" : "lightbulb"} spin={suggesting} /> AIに提案してもらう
          </button>
        </div>

        {proposal && (
          <div className="ti-proposal">
            <span className="ti-proposal-label">提案</span>
            <span className="ti-proposal-text">{proposal}</span>
            <button type="button" className="primary ti-proposal-apply" onClick={applyProposal} disabled={busy}>
              この案にする
            </button>
          </div>
        )}
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
