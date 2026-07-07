import { useEffect, useRef, useState } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { agentOf } from "../agents/registry.ts";
import { readRepoLast } from "../lib/repoLast.js";
import { readPromptHistory } from "../lib/promptHistory.js";
import { repoPromptTemplates } from "../api.js";
import { deriveBranchName, sanitizeSeg } from "../lib/reponame.js";
import type { PromptTemplateGroup } from "../api.js";
import type { KeyboardEvent } from "react";

// LaunchModal: the repo row's primary 起動 action. A small, agent-only dialog —
// pick the agent (claude/codex/opencode), a model (claude only), and an optional
// first prompt (typed, or picked from a template). On 起動 the parent creates the
// session, opens the chat mirror, and (when a prompt is present) auto-sends it once
// the session is alive.
//
// shell is deliberately absent: it has no model and no "prompt" (a shell command is a
// different, riskier semantic), so it keeps its old one-click path via the ▼ dropdown
// and the right-click menu. `kinds` is the available agent kinds, in display order.
//
// Templates come from the working copy (.claude/commands, .claude/skills,
// .agent-fleet/launch-prompts.md — GET /repos/{name}/prompt-templates) plus a local
// 履歴 group. command/skill sources are claude-flavored (slash commands / skill
// invocations), so they only show when the agent is claude. Picking a template fills
// the prompt box, expanding {{repo}}/{{branch}}/{{path}} from this repo's context.
const MODELS: [string, string][] = [
  ["", "既定"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

// LaunchOpts is what "作業を始める" hands back: the agent + optional first prompt, plus
// WHERE to run — a new isolated worktree (default) or in-place in the current working
// copy. For a worktree, base is the start point and newBranch the provisional branch
// ("" => derived from the prompt, else a wip-<slug> the user renames later).
export interface LaunchOpts {
  kind: string;
  model: string;
  prompt: string;
  worktree: boolean;
  base: string;
  newBranch: string;
}

interface LaunchModalProps {
  repo: string;
  branch?: string;
  path?: string;
  kinds: string[]; // available agent kinds (shell/ssm already excluded)
  onClose: () => void;
  onLaunch: (opts: LaunchOpts) => void;
}

export default function LaunchModal({ repo, branch, path, kinds, onClose, onLaunch }: LaunchModalProps) {
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
  // WHERE to run. Default to an isolated worktree — the safe path this whole feature is
  // about (a branch switch can't corrupt other sessions when each task has its own dir).
  // "このコピーで直接" stays available for quick in-place work on the current checkout.
  const [worktree, setWorktree] = useState(true);
  const [base, setBase] = useState(branch || "");
  const [branchName, setBranchName] = useState(""); // "" => derived from the prompt
  // Provisional branch/folder name: an explicit entry wins, else derive from the prompt;
  // shown as a preview so the user sees where the worktree lands (and can override).
  const derived = branchName.trim() || deriveBranchName(prompt);
  const folder = worktree && derived ? `${repo}@${sanitizeSeg(derived)}` : "";

  // Template sources fetched once for this repo; history is read from localStorage.
  const [srvGroups, setSrvGroups] = useState<PromptTemplateGroup[]>([]);

  useEffect(() => {
    let alive = true;
    repoPromptTemplates(repo)
      .then((d) => alive && setSrvGroups(d.groups || []))
      .catch(() => {}); // no templates dir / offline → just an empty picker
    return () => {
      alive = false;
    };
  }, [repo]);

  const hasModel = agentOf(kind).caps.model; // claude offers a model selector

  // {{repo}}/{{branch}}/{{path}} auto-embed from this repo's row context (no fill-in).
  const expand = (body: string) =>
    body
      .replaceAll("{{repo}}", repo)
      .replaceAll("{{branch}}", branch || "")
      .replaceAll("{{path}}", path || "");

  // Visible template groups: 履歴 first, then the in-repo sources. command/skill are
  // claude-flavored, so hide them for codex/opencode (their bodies wouldn't apply).
  const history = readPromptHistory(repo);
  const groups: PromptTemplateGroup[] = [
    ...(history.length
      ? [{ source: "history", label: "履歴", items: history.map((h, i) => ({ id: "h" + i, label: h, body: h })) }]
      : []),
    ...srvGroups.filter((g) => kind === "claude" || (g.source !== "command" && g.source !== "skill")),
  ];
  // Flattened for the <select>: option value = index into this list.
  const flatItems = groups.flatMap((g) => g.items.map((it) => it.body));
  const hasTemplates = flatItems.length > 0;

  const pick = (body: string) => {
    setPrompt(expand(body));
    // Focus the box so the user can immediately tweak the filled-in text.
    setTimeout(() => textRef.current?.focus(), 0);
  };

  const submit = () => {
    if (busy) return;
    setBusy(true);
    // Model only rides with an agent that has the cap; codex/opencode ignore it. For a
    // worktree, send the derived/typed provisional branch (may be "" → server picks a
    // wip-<slug>); base defaults to the parent's current branch.
    onLaunch({
      kind,
      model: hasModel ? model : "",
      prompt: prompt.trim(),
      worktree,
      base: worktree ? base.trim() : "",
      newBranch: worktree ? derived : "",
    });
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
    <Modal title={<><Icon name="play" /> 作業を始める: {repo}</>} onClose={onClose} className="launch-modal" lockClose={busy}>
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

        {/* 場所: worktree（隔離・既定）か このコピーで直接か */}
        <div className="field">
          <div className="field-label">場所</div>
          <div className="seg">
            <button
              type="button"
              className={"seg-btn" + (worktree ? " active" : "")}
              onClick={() => setWorktree(true)}
            >
              <Icon name="repo-forked" /> 新しい worktree
              <span className="seg-sub">隔離・ブランチ切替から安全</span>
            </button>
            <button
              type="button"
              className={"seg-btn" + (!worktree ? " active" : "")}
              onClick={() => setWorktree(false)}
            >
              <Icon name="repo" /> このコピーで直接
              <span className="seg-sub">現在の {branch || "作業コピー"} で作業</span>
            </button>
          </div>
          {worktree && (
            <div className="launch-wt">
              <label className="pick-field">
                <span>基点ブランチ</span>
                <input value={base} onChange={(e) => setBase(e.target.value)} placeholder={branch || "既定"} />
              </label>
              <label className="pick-field">
                <span>ブランチ名（任意）</span>
                <input
                  value={branchName}
                  onChange={(e) => setBranchName(e.target.value)}
                  placeholder={deriveBranchName(prompt) || "自動（最初の指示から）"}
                />
              </label>
              <div className="field-help">
                {folder ? (
                  <>作業コピーは <code>{folder}</code> に作成します。後でブランチ名は変更できます。</>
                ) : (
                  <>名前は最初の指示から自動で付けます（決まらなければ暫定名。後で変更可）。</>
                )}
              </div>
            </div>
          )}
        </div>

        {/* 最初のプロンプト（任意） */}
        <div className="field">
          <div className="field-label launch-prompt-label">
            <span>最初のプロンプト（任意）</span>
            {/* Template picker: fills the box from .claude/commands / skills /
                launch-prompts.md and 履歴. A native select (with optgroups) so the
                long list never gets clipped by the modal body's overflow. Resets to
                the placeholder after each pick. Hidden when nothing is available. */}
            {hasTemplates && (
              <select
                className="cinput launch-tmpl-select"
                value=""
                title="テンプレートから最初のプロンプトを挿入"
                onChange={(e) => {
                  if (e.target.value === "") return; // the placeholder row
                  const i = Number(e.target.value);
                  if (Number.isInteger(i) && flatItems[i] !== undefined) pick(flatItems[i]);
                }}
              >
                <option value="">テンプレートから挿入…</option>
                {(() => {
                  let base = 0;
                  return groups.map((g) => {
                    const start = base;
                    base += g.items.length;
                    return (
                      <optgroup key={g.source} label={g.label}>
                        {g.items.map((it, j) => (
                          <option key={g.source + ":" + it.id} value={start + j}>
                            {it.label}
                          </option>
                        ))}
                      </optgroup>
                    );
                  });
                })()}
              </select>
            )}
          </div>
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
          {busy ? "起動中…" : worktree ? "worktree で始める" : "起動"}
        </button>
      </footer>
    </Modal>
  );
}
