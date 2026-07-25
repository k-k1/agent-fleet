// LaunchModal（作業を始める）— the repo row's primary 起動 action: agent + model
// (kinds with caps.model) + optional first prompt (typed or from a template), and WHERE —
// a new isolated worktree (default; unnamed = a server-minted provisional branch
// temp/<slug> in a wip-<slug> folder) or in-place on the current checkout.
// Port of the old components/LaunchModal.
import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, ClipboardEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { agentOf } from "../../agents/registry.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { readRepoLast, resolveEffort, resolveModel, resolveStartMode } from "../../lib/repoLast.ts";
import { readPromptHistory } from "../../lib/promptHistory.ts";
import { agentLaunchDefault, useSettings } from "../../lib/settings.ts";
import { EffortPicker, ModelPicker } from "../../ui/ModelPicker.tsx";
import { repoPromptTemplates } from "./api.ts";
import type { PromptTemplateGroup } from "./api.ts";
import { sanitizeSeg } from "../../lib/reponame.ts";

// LaunchOpts: agent + optional first prompt, plus WHERE to run. For a worktree,
// base is the start point and newBranch the branch to create ("" => the server
// mints a provisional temp/<slug> the user renames later).
export interface LaunchOpts {
  kind: string;
  // driver（docs/27 P2/P3）: "managed"（共有 runtime・paneless、対応 kind の既定）|
  // ""（tui、従来）。managedDriver を持たない kind では常に ""。
  driver: string;
  model: string;
  effort: string;
  startMode: "normal" | "plan";
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
  /** The connection check hasn't settled — kinds is empty because we don't know yet,
   * not because nothing is available. Shows a 確認中 note and blocks launching. */
  settling?: boolean;
  /** Offer the "new worktree" location (default). False for a worktree row — it's
   * already an isolated checkout, so only in-place launch is offered; new worktrees
   * are created from the base clone. */
  allowWorktree?: boolean;
  /** The working copy is an SVN checkout (docs/41): it has no worktree concept, so
   * the in-place location note drops the worktree wording. */
  isSvn?: boolean;
  onClose: () => void;
  /** Present when opened from the はじめる hub: 場所を変更 returns to it. */
  onBack?: () => void;
  /** Seed for the first-prompt field (docs/21 UI刷新): the memo send modal launches a
   * new session with the composed memo text prefilled here. */
  initialPrompt?: string;
  onLaunch: (opts: LaunchOpts) => Promise<LaunchResult>;
}

export function LaunchModal({ repo, branch, path, kinds, settling = false, allowWorktree = true, isSvn = false, onClose, onBack, initialPrompt, onLaunch }: LaunchModalProps) {
  const settings = useSettings();
  const last = readRepoLast(repo);
  // Default to the last agent used in this repo when still available, else the first.
  const initialKind = last.kind && kinds.includes(last.kind) ? last.kind : kinds[0] || "claude";
  const initialDefault = agentLaunchDefault(settings, initialKind);
  const [kind, setKind] = useState(initialKind);
  // Same async-kinds hazard as StartModal: mounting while the connection check is still
  // in flight seeds the "claude" fallback above, which would stick even if claude is
  // unavailable. Re-seed once the real list lands and the held kind isn't in it.
  useEffect(() => {
    if (!kinds.length || kinds.includes(kind)) return;
    const k = kinds[0];
    const d = agentLaunchDefault(settings, k);
    setKind(k);
    setModel(resolveModel(k, repo, d.model));
    setEffort(resolveEffort(k, repo, d.effort));
    setStartMode(resolveStartMode(k, repo, d.startMode));
    setDriver(agentOf(k).managedDriver ? "managed" : "");
  }, [kinds, kind, settings, repo]);
  const tr = useT();
  // Shared per-kind priority chain (repoLast.ts resolveModel); re-resolved on a kind
  // switch so a claude tier never leaks into a codex/opencode launch (and vice versa).
  const [model, setModel] = useState(() => resolveModel(initialKind, repo, initialDefault.model));
  const [effort, setEffort] = useState(() => resolveEffort(initialKind, repo, initialDefault.effort));
  const [startMode, setStartMode] = useState(() => resolveStartMode(initialKind, repo, initialDefault.startMode));
  // ドライバ（docs/27 P2/P3）: managed 対応 kind は managed が既定（§9.2）。
  // CLI(TUI) はユーザーの明示的な選択 — セッション毎に TUI プロセス分のメモリを払う。
  const [driver, setDriver] = useState(agentOf(initialKind).managedDriver ? "managed" : "");
  const [prompt, setPrompt] = useState(initialPrompt ?? "");
  // Pasted images awaiting the launch: raw File + an object URL for the chip preview.
  // Uploaded only after the session is minted (in onStartWork), then referenced in the
  // first prompt. Agents without the imagePaste cap (shell/ssm) make paste a no-op.
  const [images, setImages] = useState<{ file: File; url: string }[]>([]);
  const imagesRef = useRef(images);
  imagesRef.current = images;
  const [busy, setBusy] = useState(false);
  const textRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null); // hidden ＋ picker (the phone path)
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
  const driverManaged = driver === "managed";
  const hasEffort = agentOf(kind).caps.effort && (driverManaged || agentOf(kind).caps.tuiEffort);
  const hasStartMode = agentOf(kind).caps.planMode && (driverManaged || agentOf(kind).caps.tuiStartMode);
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

  // Stage image File(s) as pending attachments (raw File + a preview URL). Actual upload
  // waits for the session (onStartWork). Shared by clipboard paste and the ＋ picker.
  const addImages = (files: File[]) => {
    if (!canPasteImage || !files.length) return;
    setImages((prev) => [...prev, ...files.map((f) => ({ file: f, url: URL.createObjectURL(f) }))]);
  };

  // Paste image(s) into the prompt. Non-image pastes fall through to the default (text).
  // NOTE: mobile soft keyboards (e.g. Gboard) can't commit an image into a plain
  // <textarea> — they refuse it ("paste not supported here"). The ＋ picker beside the
  // label is the phone path; it funnels into the same addImages as this handler.
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
    addImages(files);
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
      ? [{ source: "history", label: tr("launch.history"), items: history.map((h, i) => ({ id: "h" + i, label: h, body: h })) }]
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
      driver: agentOf(kind).managedDriver ? driver : "",
      model: hasModel ? model : "",
      effort: hasEffort ? effort : "",
      startMode: hasStartMode ? startMode : "normal",
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

  // Follow the shared composer send-key setting: Ctrl/⌘+Enter (default), or
  // Enter with Shift+Enter reserved for a newline.
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.metaKey || e.ctrlKey;
    const submitWithKey = settings.mirrorSend !== "enter" ? mod : !e.shiftKey && !mod;
    if (submitWithKey) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <Modal
      title={
        <>
          <Icon name="play" /> {tr("launch.title", { repo })}
        </>
      }
      onClose={onClose}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field">
          <span className="ui-field-label">{tr("launch.field.agent")}</span>
          {!kinds.length && (
            // Empty means one of two very different things: still checking, or the
            // connection check failed / nothing is authenticated. Say which — a bare
            // empty picker reads as a broken modal.
            <div className="muted launch-noagents">
              {tr(settling ? "launch.agents_checking" : "launch.agents_none")}
            </div>
          )}
          <div className="ui-seg big">
            {kinds.map((k) => {
              const a = agentOf(k);
              return (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn kind-" + a.cssClass + (kind === k ? " active" : "")}
                  onClick={() => {
                    const defaults = agentLaunchDefault(settings, k);
                    setKind(k);
                    setModel(resolveModel(k, repo, defaults.model));
                    setEffort(resolveEffort(k, repo, defaults.effort));
                    setStartMode(resolveStartMode(k, repo, defaults.startMode));
                    setDriver(a.managedDriver ? "managed" : "");
                  }}
                >
                  <Icon name={a.icon} className="seg-ic" />
                  {kindDisplayName(k)}
                  <span className="seg-sub">{tr(a.launchHintKey)}</span>
                </button>
              );
            })}
          </div>
        </div>

        {hasModel && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.model")}</span>
            <ModelPicker
              kind={kind}
              model={model}
              onChange={(next) => {
                setModel(next);
                setEffort("");
              }}
            />
          </div>
        )}

        {(hasEffort || hasStartMode) && (
          <div className="ui-field-row">
            {hasEffort && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("launch.field.effort")}</span>
                <EffortPicker kind={kind} model={model} effort={effort} onChange={setEffort} />
                <span className="ui-field-hint">{tr("launch.field.effort_hint")}</span>
              </div>
            )}
            {hasStartMode && (
              <div className="ui-field">
                <span className="ui-field-label">{tr("launch.field.start_mode")}</span>
                <select value={startMode} onChange={(e) => setStartMode(e.target.value === "plan" ? "plan" : "normal")}>
                  <option value="normal">{agentOf(kind).defaultModeLabel || tr("launch.mode_normal")}</option>
                  <option value="plan">Plan</option>
                </select>
                <span className="ui-field-hint">{tr("launch.plan_hint")}</span>
              </div>
            )}
          </div>
        )}

        {/* ドライバ（docs/27 P2/P3）: managed 対応 kind だけに出す。既定は
            managed（共有 runtime・paneless・省メモリ）。CLI(TUI) はターミナル操作が
            必要な人向けの明示的なメモリトレードオフ（セッション毎に TUI プロセス分）。 */}
        {agentOf(kind).managedDriver && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.driver")}</span>
            <div className="ui-seg">
              <button
                type="button"
                className={"seg-btn" + (driver === "managed" ? " active" : "")}
                onClick={() => setDriver("managed")}
              >
                <Icon name="server-process" /> {tr("launch.driver_managed")}
                <span className="seg-sub">{tr("launch.driver_managed_sub")}</span>
              </button>
              <button
                type="button"
                className={"seg-btn" + (driver !== "managed" ? " active" : "")}
                onClick={() => setDriver("")}
              >
                <Icon name="terminal" /> {tr("launch.driver_terminal")}
                <span className="seg-sub">
                  {tr("launch.tui_note", { cost: tr("common.approx", { v: agentOf(kind).tuiMemoryCost }) })}
                </span>
              </button>
            </div>
          </div>
        )}

        {/* 場所: worktree（隔離・既定）か このコピーで直接か。worktree 行では選択肢
            なし（この worktree 内で直接起動）。 */}
        {!allowWorktree ? (
          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.location")}</span>
            <span className="ui-field-hint">
              {isSvn ? (
                tr("launch.svn_direct_note")
              ) : (
                <Trans k="launch.worktree_direct_note" vars={{ branch: branch || tr("launch.current_wc") }} components={[<code />]} />
              )}
            </span>
          </div>
        ) : (
        <div className="ui-field">
          <span className="ui-field-label">{tr("launch.field.location")}</span>
          <div className="ui-seg">
            <button type="button" className={"seg-btn" + (worktree ? " active" : "")} onClick={() => setWorktree(true)}>
              <Icon name="repo-forked" /> {tr("launch.new_worktree")}
              <span className="seg-sub">{tr("launch.new_worktree_sub")}</span>
            </button>
            <button type="button" className={"seg-btn" + (!worktree ? " active" : "")} onClick={() => setWorktree(false)}>
              <Icon name="repo" /> {tr("launch.direct_here")}
              <span className="seg-sub">{tr("launch.direct_here_sub", { branch: branch || tr("launch.wc") })}</span>
            </button>
          </div>
          {worktree && (
            <>
              <label className="ui-field">
                <span className="ui-field-label">{tr("launch.base_branch")}</span>
                <input value={base} onChange={(e) => setBase(e.target.value)} placeholder={branch || tr("launch.base_default")} />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">{tr("launch.branch_name")}</span>
                <input
                  value={branchName}
                  onChange={(e) => {
                    setBranchName(e.target.value);
                    setConflict(null);
                  }}
                  placeholder={tr("launch.branch_ph")}
                />
              </label>
              <span className="ui-field-hint">
                {folder ? (
                  <Trans k="launch.wc_created_note" vars={{ folder }} components={[<code />]} />
                ) : (
                  tr("launch.branch_empty_note")
                )}
              </span>
              {conflict && (
                <div className="launch-conflict">
                  {conflict === "local" ? (
                    <span>
                      <Trans k="launch.local_branch_exists" vars={{ branch: newBranch }} components={[<code />]} />
                    </span>
                  ) : (
                    <span>
                      <Trans k="launch.remote_branch_exists" vars={{ branch: newBranch }} components={[<code />]} />
                    </span>
                  )}
                  {conflict === "remote" && (
                    <Button small icon="git-branch" disabled={busy} onClick={() => void start(true)}>
                      {tr("launch.work_existing_branch")}
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
            <span>{tr("launch.first_prompt")}</span>
            <span className="launch-prompt-tools">
              {/* ＋ attach: the paste-less path. Mobile keyboards can't paste images into
                  a <textarea>, so a phone can only attach through this picker. */}
              {canPasteImage && (
                <>
                  <input
                    ref={fileRef}
                    type="file"
                    accept="image/*"
                    multiple
                    hidden
                    onChange={(e) => {
                      const files = Array.from(e.target.files || []).filter((f) => f.type.startsWith("image/"));
                      e.target.value = ""; // allow re-picking the same file
                      addImages(files);
                    }}
                  />
                  <button
                    type="button"
                    className="ghost launch-attach-btn"
                    title={tr("launch.attach_image")}
                    onClick={() => fileRef.current?.click()}
                  >
                    <Icon name="add" />
                  </button>
                </>
              )}
              {hasTemplates && (
                <select
                  className="launch-tmpl-select"
                  value=""
                  title={tr("launch.template_insert_title")}
                  onChange={(e) => {
                    if (e.target.value === "") return;
                    const i = Number(e.target.value);
                    if (Number.isInteger(i) && flatItems[i] !== undefined) pick(flatItems[i]);
                  }}
                >
                  <option value="">{tr("launch.template_insert")}</option>
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
          </span>
          {images.length > 0 && (
            <div className="mirror-attach">
              {images.map((im, i) => (
                <div className="ma-chip" key={im.url}>
                  <img className="ma-thumb" src={im.url} alt="" />
                  <button type="button" className="ma-del" title={tr("common.delete")} onClick={() => removeImage(i)}>
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
            placeholder={tr("launch.first_prompt_ph")}
          />
          <span className="ui-field-hint">
            {tr("launch.first_prompt_note")}
            {canPasteImage && tr("launch.image_paste_note")}
          </span>
        </div>
      </div>

      <footer className="ui-modal-foot">
        {onBack && (
          <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={onBack} disabled={busy}>
            {tr("launch.change_location")}
          </Button>
        )}
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" onClick={submit} disabled={busy || !kinds.length}>
          {busy ? tr("launch.launching") : worktree ? tr("launch.start_worktree") : tr("launch.launch")}
        </Button>
      </footer>
    </Modal>
  );
}
