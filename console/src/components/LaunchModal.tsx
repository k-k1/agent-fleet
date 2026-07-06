import { useRef, useState } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { agentOf } from "../agents/registry.ts";
import { readRepoLast } from "../lib/repoLast.js";
import type { KeyboardEvent } from "react";

// LaunchModal: the repo row's primary 起動 action. A small, agent-only dialog —
// pick the agent (claude/codex/opencode), a model (claude only), and an optional
// first prompt. On 起動 the parent creates the session, opens the chat mirror, and
// (when a prompt was typed) auto-sends it once the session is alive.
//
// shell is deliberately absent: it has no model and no "prompt" (a shell command is a
// different, riskier semantic), so it keeps its old one-click path via the ▼ dropdown
// and the right-click menu. `kinds` is the available agent kinds, in display order.
const MODELS: [string, string][] = [
  ["", "既定"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

interface LaunchModalProps {
  repo: string;
  kinds: string[]; // available agent kinds (shell/ssm already excluded)
  onClose: () => void;
  onLaunch: (kind: string, model: string, prompt: string) => void;
}

export default function LaunchModal({ repo, kinds, onClose, onLaunch }: LaunchModalProps) {
  const last = readRepoLast(repo);
  // Default to the last agent used in this repo when it's still an available agent
  // kind; otherwise the first available agent (usually claude). Falls back to the
  // first kind if the remembered one is gone (e.g. was shell, or its conn dropped).
  const initialKind = last.kind && kinds.includes(last.kind) ? last.kind : kinds[0] || "claude";
  const [kind, setKind] = useState(initialKind);
  const [model, setModel] = useState(last.model || "");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const textRef = useRef<HTMLTextAreaElement>(null);

  const hasModel = agentOf(kind).caps.model; // claude offers a model selector

  const submit = () => {
    if (busy) return;
    setBusy(true);
    // Model only rides with an agent that has the cap; codex/opencode ignore it.
    onLaunch(kind, hasModel ? model : "", prompt.trim());
    // The parent opens the session + closes us; keep the button busy meanwhile so a
    // double-Enter can't fire two launches.
  };

  // ⌘/Ctrl+Enter submits from the prompt box (plain Enter newlines — the prompt is
  // free-form and may be multi-line; the 起動 button is the primary submit).
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <Modal title={<><Icon name="play" /> 起動: {repo}</>} onClose={onClose} className="launch-modal" lockClose={busy}>
      <div className="modal-body">
        {/* エージェント */}
        <div className="field">
          <div className="field-label">エージェント</div>
          <div className="seg big">
            {kinds.map((k) => {
              const a = agentOf(k);
              return (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn kind-" + a.cssClass + (kind === k ? " active" : "")}
                  onClick={() => setKind(k)}
                >
                  <Icon name={a.icon} className="seg-ic" />
                  {a.label}
                  <span className="seg-sub">{a.launchHint}</span>
                </button>
              );
            })}
          </div>
        </div>

        {/* モデル（claude のみ） */}
        {hasModel && (
          <div className="field">
            <div className="field-label">モデル</div>
            <div className="seg">
              {MODELS.map(([v, label]) => (
                <button
                  key={v || "default"}
                  type="button"
                  className={"seg-btn" + (model === v ? " active" : "")}
                  onClick={() => setModel(v)}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* 最初のプロンプト（任意） */}
        <div className="field">
          <div className="field-label">最初のプロンプト（任意）</div>
          <textarea
            ref={textRef}
            className="cinput launch-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={onKey}
            rows={4}
            autoFocus
            placeholder="起動後にこのプロンプトを送信します。空なら送信せず開くだけ。"
          />
          <div className="field-help">
            セッション起動後、準備でき次第この内容を1回だけ自動送信します（⌘/Ctrl+Enter で起動）。
          </div>
        </div>
      </div>

      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose} disabled={busy}>
          キャンセル
        </button>
        <button type="button" className="primary" onClick={submit} disabled={busy}>
          {busy ? "起動中…" : "起動"}
        </button>
      </footer>
    </Modal>
  );
}
