import { useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { AGENTS } from "../agents/registry.ts";
import { SESSION_KINDS } from "../types/session.ts";
import type { SessionKind } from "../types/session.ts";
import type { Assistant, AssistantInput, ToolGrant } from "../types/assistant.ts";

// AssistantModal creates or edits a user-defined assistant template (docs/19 Q2):
// name, backend agent, optional model, a persona (system prompt), a tool grant, and
// optional knowledge dirs. Builtins are never edited here (the section hides edit for
// them), so this form always drives a user assistant.

// Chat-capable agent kinds, data-driven off the registry cap (same source as the New
// Chat picker) — codex lights up automatically once headlessChat flips (Phase A.2).
const chatKinds: SessionKind[] = SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat);

// A small palette of codicons a user can tag an assistant with (kept short on purpose).
const ICONS = ["comment-discussion", "sparkle", "rocket", "globe", "book", "beaker", "heart", "star-full"];

const TOOLS: { value: ToolGrant; label: string; help: string }[] = [
  { value: "none", label: "なし", help: "外部ツールなし。チャットで直接回答します。" },
  {
    value: "af_read",
    label: "AF 読み取り",
    help: "自分のワークスペースのセッション一覧・状態・出力を読み取れます（書き込みは不可）。",
  },
  {
    value: "af_write",
    label: "AF 書き込み",
    help: "読み取りに加え、セッションへプロンプトを送信できます（作業の代行）。信頼できる用途にのみ許可してください。",
  },
];

interface AssistantModalProps {
  initial?: Assistant | null; // present = edit; absent = create
  onClose: () => void;
  onSave: (input: AssistantInput) => Promise<void>;
}

export default function AssistantModal({ initial, onClose, onSave }: AssistantModalProps) {
  const [name, setName] = useState(initial?.name ?? "");
  const [icon, setIcon] = useState(initial?.icon ?? ICONS[0]);
  const [agent, setAgent] = useState<SessionKind>(initial?.agent ?? chatKinds[0] ?? "claude");
  const [model, setModel] = useState(initial?.model ?? "");
  const [persona, setPersona] = useState(initial?.persona ?? "");
  const [tools, setTools] = useState<ToolGrant>(initial?.tools ?? "none");
  const [knowledge, setKnowledge] = useState((initial?.knowledge ?? []).join("\n"));
  const [busy, setBusy] = useState(false);

  const canSubmit = name.trim().length > 0 && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      await onSave({
        name: name.trim(),
        icon,
        agent,
        model: model.trim(),
        persona: persona.trim(),
        tools,
        knowledge: knowledge
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean),
      });
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={initial ? "アシスタントを編集" : "アシスタントを作成"}
      onClose={onClose}
      className="session-modal"
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="modal-body">
        <div className="field">
          <div className="field-label">名前</div>
          <label className="pick-field">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例: リリースノート翻訳" autoFocus />
          </label>
        </div>

        <div className="field">
          <div className="field-label">アイコン</div>
          <div className="assistant-icon-pick">
            {ICONS.map((ic) => (
              <button
                key={ic}
                type="button"
                className={"icon assistant-icon-opt" + (icon === ic ? " active" : "")}
                title={ic}
                onClick={() => setIcon(ic)}
              >
                <Icon name={ic} />
              </button>
            ))}
          </div>
        </div>

        {chatKinds.length > 1 && (
          <div className="field">
            <div className="field-label">エージェント</div>
            <div className="seg">
              {chatKinds.map((k) => (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn" + (agent === k ? " active" : "")}
                  onClick={() => setAgent(k)}
                >
                  {AGENTS[k].assistantName}
                </button>
              ))}
            </div>
          </div>
        )}

        <div className="field">
          <div className="field-label">モデル（任意）</div>
          <label className="pick-field">
            <input value={model} onChange={(e) => setModel(e.target.value)} placeholder="既定モデル" />
          </label>
        </div>

        <div className="field">
          <div className="field-label">ペルソナ（システムプロンプト）</div>
          <div className="field-help">アシスタントの役割・口調・制約を書きます。空欄なら汎用アシスタントとして動きます。</div>
          <label className="pick-field">
            <textarea
              className="assistant-persona"
              value={persona}
              onChange={(e) => setPersona(e.target.value)}
              rows={5}
              placeholder="例: あなたは技術文書の翻訳者です。訳文のみを返してください。"
            />
          </label>
        </div>

        <div className="field">
          <div className="field-label">ツール許可</div>
          <div className="seg">
            {TOOLS.map((t) => (
              <button
                key={t.value}
                type="button"
                className={"seg-btn" + (tools === t.value ? " active" : "")}
                onClick={() => setTools(t.value)}
              >
                {t.label}
              </button>
            ))}
          </div>
          <div className="field-help">{TOOLS.find((t) => t.value === tools)?.help}</div>
        </div>

        <div className="field">
          <div className="field-label">知識ディレクトリ（任意・1行に1つ）</div>
          <div className="field-help">
            参照させたいドキュメントのあるディレクトリ（コンテナ内の絶対パス）を指定すると、会話で読み取れます。
          </div>
          <label className="pick-field">
            <textarea
              className="assistant-knowledge"
              value={knowledge}
              onChange={(e) => setKnowledge(e.target.value)}
              rows={2}
              placeholder="/home/dev/repos/my-repo/docs"
            />
          </label>
        </div>
      </div>

      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose}>
          キャンセル
        </button>
        <button type="submit" className="primary" disabled={!canSubmit}>
          {initial ? "保存" : "作成"}
        </button>
      </footer>
    </Modal>
  );
}
