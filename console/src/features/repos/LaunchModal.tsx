// LaunchModal（作業を始める）— the repo row's primary 起動 action: agent + model
// (claude only) + optional first prompt (typed or from a template), and WHERE —
// a new isolated worktree (default; unnamed = a server-minted provisional branch
// temp/<slug> in a wip-<slug> folder) or in-place on the current checkout.
// Port of the old components/LaunchModal.
import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, ClipboardEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { agentOf } from "../../agents/registry.ts";
import { readRepoLast } from "../../lib/repoLast.ts";
import { readPromptHistory } from "../../lib/promptHistory.ts";
import { repoPromptTemplates } from "./api.ts";
import type { PromptTemplateGroup } from "./api.ts";
import { sanitizeSeg } from "../../lib/reponame.ts";

const MODELS: [string, string][] = [
  ["", "既定"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

// LaunchOpts: agent + optional first prompt, plus WHERE to run. For a worktree,
// base is the start point and newBranch the branch to create ("" => the server
// mints a provisional temp/<slug> the user renames later).
export interface LaunchOpts {
  kind: string;
  model: string;
  prompt: string;
  // Pasted images awaiting upload. Held as raw Files (not yet uploaded) because no
  // session exists at compose time — the caller uploads them once the session is
  // minted and embeds the saved paths into the first prompt (claude Read-tool flow).
  images: File[];
  worktree: boolean;
  base: string;
  newBranch: string;
  useExisting?: boolean; // check out the existing branch instead of creating one
}

// LaunchResult: close on ok, else stay open and offer a fix for a name collision.
export interface LaunchResult {
  ok: boolean;
  conflict?: "local" | "remote";
}

interface LaunchModalProps {
  repo: string;
  branch?: string;
  path?: string;
  kinds: string[]; // available agent kinds (shell/ssm already excluded)
  /** Offer the "new worktree" location (default). False for a worktree row — it's
   * already an isolated checkout, so only in-place launch is offered; new worktrees
   * are created from the base clone. */
  allowWorktree?: boolean;
  onClose: () => void;
  onLaunch: (opts: LaunchOpts) => Promise<LaunchResult>;
}

export function LaunchModal({ repo, branch, path, kinds, allowWorktree = true, onClose, onLaunch }: LaunchModalProps) {
  const last = readRepoLast(repo);
  // Default to the last agent used in this repo when still available, else the first.
  const initialKind = last.kind && kinds.includes(last.kind) ? last.kind : kinds[0] || "claude";
  const [kind, setKind] = useState(initialKind);
  const [model, setModel] = useState(last.model || "");
  const [prompt, setPrompt] = useState("");
  // Pasted images awaiting the launch: raw File + an object URL for the chip preview.
  // Uploaded only after the session is minted (in onStartWork), then referenced in the
  // first prompt. Non-claude agents lack the imagePaste cap, so paste is a no-op there.
  const [images, setImages] = useState<{ file: File; url: string }[]>([]);
  const imagesRef = useRef(images);
  imagesRef.current = images;
  const [busy, setBusy] = useState(false);
  const textRef = useRef<HTMLTextAreaElement>(null);
  // WHERE: default to an isolated worktree — a branch switch can't corrupt other
  // sessions when each task has its own dir. From a worktree row (allowWorktree
  // false) there's no worktree choice: launch in place.
  const [worktree, setWorktree] = useState(allowWorktree);
  const [base, setBase] = useState(branch || "");
  const [branchName, setBranchName] = useState(""); // "" => derived from the prompt
  const [conflict, setConflict] = useState<"local" | "remote" | null>(null);
  // Naming: an explicit entry wins as-is (branch == folder); otherwise the server
  // mints a throwaway temp/<slug> in a wip-<slug> folder. The prompt text never
  // feeds the name — deriving words from it produced odd names on mixed-language
  // prompts, so provisional naming is always the slug.
  const explicit = branchName.trim();
  const newBranch = explicit; // "" → server picks temp/<slug>
  const folder = worktree && explicit ? `${repo}@${sanitizeSeg(explicit)}` : "";

  // Template sources fetched once for this repo; 履歴 comes from localStorage.
  const [srvGroups, setSrvGroups] = useState<PromptTemplateGroup[]>([]);
  useEffect(() => {
    let alive = true;
    repoPromptTemplates(repo)
      .then((d) => alive && setSrvGroups(d.groups || []))
      .catch(() => {}); // no templates dir / offline → empty picker
    return () => {
      alive = false;
    };
  }, [repo]);

  const hasModel = agentOf(kind).caps.model;
  const canPasteImage = agentOf(kind).caps.imagePaste;

  // Revoke every held preview URL when the modal unmounts (avoids leaking object URLs).
  useEffect(() => () => imagesRef.current.forEach((x) => URL.revokeObjectURL(x.url)), []);

  // Switching to an agent without image support drops any staged images (they'd have
  // nowhere to go). Runs only on that transition.
  useEffect(() => {
    if (!canPasteImage)
      setImages((prev) => {
        prev.forEach((x) => URL.revokeObjectURL(x.url));
        return prev.length ? [] : prev;
      });
  }, [canPasteImage]);

  // Paste image(s) into the prompt: stage each File + a preview URL. Actual upload waits
  // for the session (onStartWork). Non-image pastes fall through to the default (text).
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    if (!canPasteImage) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === "file" && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (!files.length) return; // ordinary text paste — let it happen
    e.preventDefault();
    setImages((prev) => [...prev, ...files.map((f) => ({ file: f, url: URL.createObjectURL(f) }))]);
  };

  const removeImage = (i: number) =>
    setImages((prev) => {
      if (prev[i]) URL.revokeObjectURL(prev[i].url);
      return prev.filter((_, idx) => idx !== i);
    });

  // {{repo}}/{{branch}}/{{path}} auto-embed from this repo's row context.
  const expand = (body: string) =>
    body.replaceAll("{{repo}}", repo).replaceAll("{{branch}}", branch || "").replaceAll("{{path}}", path || "");

  // 履歴 first, then in-repo sources. command/skill are claude-flavored.
  const history = readPromptHistory(repo);
  const groups: PromptTemplateGroup[] = [
    ...(history.length
      ? [{ source: "history", label: "履歴", items: history.map((h, i) => ({ id: "h" + i, label: h, body: h })) }]
      : []),
    ...srvGroups.filter((g) => kind === "claude" || (g.source !== "command" && g.source !== "skill")),
  ];
  const flatItems = groups.flatMap((g) => g.items.map((it) => it.body));
  const hasTemplates = flatItems.length > 0;

  const pick = (body: string) => {
    setPrompt(expand(body));
    setTimeout(() => textRef.current?.focus(), 0);
  };

  // start fires the launch; a name collision keeps the modal open with a fix
  // panel. useExisting re-runs the launch to check out the colliding branch.
  const start = async (useExisting: boolean) => {
    if (busy) return;
    setBusy(true);
    setConflict(null);
    const r = await onLaunch({
      kind,
      model: hasModel ? model : "",
      prompt: prompt.trim(),
      images: canPasteImage ? images.map((x) => x.file) : [],
      worktree,
      base: worktree ? (useExisting ? newBranch : base.trim()) : "",
      newBranch: worktree && !useExisting ? newBranch : "",
      useExisting,
    });
    if (r?.ok) {
      onClose();
      return;
    }
    setBusy(false);
    setConflict(r?.conflict ?? null);
  };
  const submit = () => void start(false);

  // ⌘/Ctrl+Enter submits from the prompt box (plain Enter newlines).
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <Modal
      title={
        <>
          <Icon name="play" /> 作業を始める: {repo}
        </>
      }
      onClose={onClose}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field">
          <span className="ui-field-label">エージェント</span>
          <div className="ui-seg big">
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

        {hasModel && (
          <div className="ui-field">
            <span className="ui-field-label">モデル</span>
            <div className="ui-seg">
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

        {/* 場所: worktree（隔離・既定）か このコピーで直接か。worktree 行では選択肢
            なし（この worktree 内で直接起動）。 */}
        {!allowWorktree ? (
          <div className="ui-field">
            <span className="ui-field-label">場所</span>
            <span className="ui-field-hint">
              この worktree（<code>{branch || "現在の作業コピー"}</code>）で直接起動します。新しい worktree
              はベースのリポジトリから作成してください。
            </span>
          </div>
        ) : (
        <div className="ui-field">
          <span className="ui-field-label">場所</span>
          <div className="ui-seg">
            <button type="button" className={"seg-btn" + (worktree ? " active" : "")} onClick={() => setWorktree(true)}>
              <Icon name="repo-forked" /> 新しい worktree
              <span className="seg-sub">隔離・ブランチ切替から安全</span>
            </button>
            <button type="button" className={"seg-btn" + (!worktree ? " active" : "")} onClick={() => setWorktree(false)}>
              <Icon name="repo" /> このコピーで直接
              <span className="seg-sub">現在の {branch || "作業コピー"} で作業</span>
            </button>
          </div>
          {worktree && (
            <>
              <label className="ui-field">
                <span className="ui-field-label">基点ブランチ</span>
                <input value={base} onChange={(e) => setBase(e.target.value)} placeholder={branch || "既定"} />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">ブランチ名（任意）</span>
                <input
                  value={branchName}
                  onChange={(e) => {
                    setBranchName(e.target.value);
                    setConflict(null);
                  }}
                  placeholder="自動（暫定名 temp/…）"
                />
              </label>
              <span className="ui-field-hint">
                {folder ? (
                  <>
                    作業コピーは <code>{folder}</code> に作成します。後でブランチ名は変更できます。
                  </>
                ) : (
                  <>空なら暫定名（temp/…）で始めます。ブランチ名は後で変更できます。</>
                )}
              </span>
              {conflict && (
                <div className="launch-conflict">
                  {conflict === "local" ? (
                    <span>
                      同名のローカルブランチ <code>{newBranch}</code> が既にあります。別の名前にしてください。
                    </span>
                  ) : (
                    <span>
                      リモートに同名ブランチ <code>{newBranch}</code> があります（過去のブランチ）。別名にするか、
                      その既存ブランチで作業できます。
                    </span>
                  )}
                  {conflict === "remote" && (
                    <Button small icon="git-branch" disabled={busy} onClick={() => void start(true)}>
                      その既存ブランチで作業
                    </Button>
                  )}
                </div>
              )}
            </>
          )}
        </div>
        )}

        {/* 最初のプロンプト（任意） */}
        <div className="ui-field">
          <span className="ui-field-label launch-prompt-label">
            <span>最初のプロンプト（任意）</span>
            {hasTemplates && (
              <select
                className="launch-tmpl-select"
                value=""
                title="テンプレートから最初のプロンプトを挿入"
                onChange={(e) => {
                  if (e.target.value === "") return;
                  const i = Number(e.target.value);
                  if (Number.isInteger(i) && flatItems[i] !== undefined) pick(flatItems[i]);
                }}
              >
                <option value="">テンプレートから挿入…</option>
                {(() => {
                  let idx = 0;
                  return groups.map((g) => {
                    const start = idx;
                    idx += g.items.length;
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
          </span>
          {images.length > 0 && (
            <div className="mirror-attach">
              {images.map((im, i) => (
                <div className="ma-chip" key={im.url}>
                  <img className="ma-thumb" src={im.url} alt="" />
                  <button type="button" className="ma-del" title="削除" onClick={() => removeImage(i)}>
                    <Icon name="close" />
                  </button>
                </div>
              ))}
            </div>
          )}
          <textarea
            ref={textRef}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={onKey}
            onPaste={onPaste}
            rows={4}
            autoFocus
            placeholder="起動後にこのプロンプトを送信します。空なら送信せず開くだけ。"
          />
          <span className="ui-field-hint">
            セッション起動後、準備でき次第この内容を1回だけ自動送信します（⌘/Ctrl+Enter で起動）。
            {canPasteImage && "画像はここに貼り付けられます。"}
          </span>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          キャンセル
        </Button>
        <Button variant="primary" onClick={submit} disabled={busy}>
          {busy ? "起動中…" : worktree ? "worktree で始める" : "起動"}
        </Button>
      </footer>
    </Modal>
  );
}
