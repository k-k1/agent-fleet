// SessionTitleModal — manual title edit (⋯ → タイトルを変更). "AIに提案" fetches a
// candidate via the preview-only /title/suggest endpoint and shows it as a
// proposal the user applies explicitly — it never clobbers what they typed.
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";

interface SessionTitleModalProps {
  name: string;
  title: string;
  onClose: () => void;
  onSaved: () => void;
}

export function SessionTitleModal({ name, title, onClose, onSaved }: SessionTitleModalProps) {
  const [value, setValue] = useState(title);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
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
      if (typeof j.suggestedTitle === "string" && j.suggestedTitle) setProposal(j.suggestedTitle);
    } catch {
      toast("提案の取得に失敗しました（通信エラー）");
    } finally {
      setSuggesting(false);
    }
  };

  return (
    <Modal title="タイトルを変更" onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">タイトル</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder="例: セッションタイトルの自動提案"
            maxLength={80}
            autoFocus
          />
          <span className="ui-field-hint">未入力のまま保存すると自動命名（リポジトリ名＋日時）に戻ります。</span>
        </label>
        <div>
          <Button icon={suggesting ? "loading" : "lightbulb"} onClick={suggest} disabled={busy}>
            AIに提案してもらう
          </Button>
        </div>
        {proposal && (
          <div className="sm-proposal">
            <span className="sm-proposal-label">提案</span>
            <span className="sm-proposal-text">{proposal}</span>
            <Button
              small
              variant="primary"
              disabled={busy}
              onClick={() => {
                setValue(proposal);
                setProposal("");
              }}
            >
              この案にする
            </Button>
          </div>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          キャンセル
        </Button>
        <Button variant="primary" type="submit" disabled={saving}>
          保存
        </Button>
      </footer>
    </Modal>
  );
}
