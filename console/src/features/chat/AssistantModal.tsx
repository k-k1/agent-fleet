import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { AGENTS } from "../../agents/registry.ts";
import { SESSION_KINDS } from "../../types/session.ts";
import type { SessionKind } from "../../types/session.ts";
import type { Assistant, AssistantInput, ToolGrant } from "../../types/assistant.ts";
import { readerVoiceChoices } from "./tts.ts";
import { loadSpeakers } from "./ttsSpeakers.ts";

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

export function AssistantModal({ initial, onClose, onSave }: AssistantModalProps) {
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [icon, setIcon] = useState(initial?.icon ?? ICONS[0]);
  const [agent, setAgent] = useState<SessionKind>(initial?.agent ?? chatKinds[0] ?? "claude");
  const [model, setModel] = useState(initial?.model ?? "");
  const [persona, setPersona] = useState(initial?.persona ?? "");
  const [tools, setTools] = useState<ToolGrant>(initial?.tools ?? "none");
  const [knowledge, setKnowledge] = useState((initial?.knowledge ?? []).join("\n"));
  const [voice, setVoice] = useState(initial?.voice ?? "");
  const [busy, setBusy] = useState(false);
  // 声の選択肢はキャラクター設定×エンジン実カタログ（tts.ts）。届いたら再レンダ。
  const [, setCatalogLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setCatalogLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  // 先頭の「設定の話者」を「自動」に読み替える（アシスタントの "" = プールから自動割り当て）。
  const voiceChoices: [string, string][] = [["", "自動（キャラプールから）"], ...readerVoiceChoices().slice(1)];
  if (voice && !voiceChoices.some(([v]) => v === voice)) voiceChoices.push([voice, voice]); // プール外の保存値も表示

  const canSubmit = name.trim().length > 0 && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      await onSave({
        name: name.trim(),
        description: description.trim(),
        icon,
        agent,
        model: model.trim(),
        persona: persona.trim(),
        tools,
        knowledge: knowledge
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean),
        voice,
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
      <div className="ui-modal-body">
        <div className="ui-field">
          <div className="ui-field-label">名前</div>
          <label className="ui-field">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例: リリースノート翻訳" autoFocus />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">説明（会話開始時の挨拶）</div>
          <div className="ui-field-hint">会話を始める前に表示される自己紹介です。何ができるかを一言で。</div>
          <label className="ui-field">
            <textarea
              className="assistant-persona"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              placeholder="例: 文章を渡してください。日本語↔英語を翻訳します。"
            />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">アイコン</div>
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
          <div className="ui-field">
            <div className="ui-field-label">エージェント</div>
            <div className="ui-seg">
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

        <div className="ui-field">
          <div className="ui-field-label">モデル（任意）</div>
          <label className="ui-field">
            <input value={model} onChange={(e) => setModel(e.target.value)} placeholder="既定モデル" />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">読み上げの声（任意）</div>
          <div className="ui-field-hint">
            このアシスタントの回答を読み上げるときの声。「自動」は読み上げ設定のキャラプールから固定で
            割り当てます（「セッションごとに声を変える」が ON のとき。OFF なら設定の話者）。
          </div>
          <label className="ui-field">
            <select value={voice} onChange={(e) => setVoice(e.target.value)}>
              {voiceChoices.map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">ペルソナ（システムプロンプト）</div>
          <div className="ui-field-hint">アシスタントの役割・口調・制約を書きます。空欄なら汎用アシスタントとして動きます。</div>
          <label className="ui-field">
            <textarea
              className="assistant-persona"
              value={persona}
              onChange={(e) => setPersona(e.target.value)}
              rows={5}
              placeholder="例: あなたは技術文書の翻訳者です。訳文のみを返してください。"
            />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">ツール許可</div>
          <div className="ui-seg">
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
          <div className="ui-field-hint">{TOOLS.find((t) => t.value === tools)?.help}</div>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">知識ディレクトリ（任意・1行に1つ）</div>
          <div className="ui-field-hint">
            参照させたいドキュメントのあるディレクトリ（コンテナ内の絶対パス）を指定すると、会話で読み取れます。
          </div>
          <label className="ui-field">
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

      <footer className="ui-modal-foot">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose}>
          キャンセル
        </button>
        <button type="submit" className="ui-btn ui-btn-primary" disabled={!canSubmit}>
          {initial ? "保存" : "作成"}
        </button>
      </footer>
    </Modal>
  );
}
