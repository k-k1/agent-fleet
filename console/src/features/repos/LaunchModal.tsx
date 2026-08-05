// LaunchModal（作業を始める）— the repo row's primary 起動 action: agent + model
// (kinds with caps.model) + optional first prompt (typed or from a template), and WHERE —
// a new isolated worktree (default; unnamed = a server-minted provisional branch
// temp/<slug> in a wip-<slug> folder) or in-place on the current checkout.
// Port of the old components/LaunchModal.
//
// レイアウト（2026-08 整理）: 11 個のフィールドが同格に並んでいたのをやめ、
//   ①今回決めること（エージェント → モデル → 最初のプロンプト）を常時表示
//   ②場所（worktree / ブランチ / 基点）と ③詳細（実行方式・effort・開始モード・
//     作業ディレクトリ・セッション名）は「要約 1 行 ＋ 展開」
// の 3 層にしてある。既定値は repoLast がリポジトリ毎に覚えていて大半は正しいので、
// 畳んだ側は "確認できれば十分" が成り立つ。要約は必ず**実際に起動される値**を出す
// こと（畳んだせいで見えない設定で起動された、が起きないように）。
import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, ClipboardEvent, ReactNode } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { agentOf } from "../../agents/registry.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { readRepoLast, resolveEffort, resolveModel, resolveStartMode, resolveSubdir } from "../../lib/repoLast.ts";
import { readPromptHistory } from "../../lib/promptHistory.ts";
import { agentLaunchDefault, useSettings } from "../../lib/settings.ts";
import { useEffortOptions } from "../../lib/agentModels.ts";
import { EffortPicker, ModelPicker } from "../../ui/ModelPicker.tsx";
import { readLaunchOpen, writeLaunchOpen } from "./launchPrefs.ts";
import type { LaunchSectionKey } from "./launchPrefs.ts";
import { repoPromptTemplates } from "./api.ts";
import type { PromptTemplateGroup } from "./api.ts";
import { api } from "../../core/api/client.ts";
import { BranchList } from "./BranchList.tsx";
import { SubdirPicker } from "./SubdirPicker.tsx";
import type { Branch } from "./BranchList.tsx";
import { sanitizeSeg } from "../../lib/reponame.ts";
import { coarsePointer } from "../../lib/device.ts";

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
	/** Optional user-visible session name, supplied by a handoff proposal or edited here. */
  title: string;
  // Pasted images awaiting upload. Held as raw Files (not yet uploaded) because no
  // session exists at compose time — the caller uploads them once the session is
  // minted and embeds the saved paths into the first prompt (claude Read-tool flow).
  images: File[];
  worktree: boolean;
  /** Folder INSIDE the working copy to start the agent in, slash-relative
   * ("" = the working copy root). For a worktree launch it is resolved inside the
   * freshly created worktree, not the parent. */
  subdir: string;
  base: string;
  newBranch: string;
  useExisting?: boolean; // check out the existing branch instead of creating one
}

// LaunchResult: close on ok, else stay open and offer a fix for a name collision.
// "in_use" is not a naming problem but a git one — the branch is checked out in
// another working copy — so it carries that copy's folder instead of a fix button.
export interface LaunchResult {
  ok: boolean;
  conflict?: "local" | "remote" | "in_use";
  worktree?: string;
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
  /** Present when opened from the はじめる hub: はじめる に戻る returns to it. */
  onBack?: () => void;
  /** Seed for the first-prompt field (docs/21 UI刷新): the memo send modal launches a
   * new session with the composed memo text prefilled here. */
  initialPrompt?: string;
	/** Optional initial session title, e.g. proposed by a predecessor session. */
  initialTitle?: string;
  /** Open straight into 既存ブランチ mode with this branch picked — the SCM view's
   * "start work on this branch" actions land here. */
  initialExistingBranch?: string;
  onLaunch: (opts: LaunchOpts) => Promise<LaunchResult>;
}

interface LaunchSectionProps {
  label: string;
  /** What this section will actually do, shown while it is collapsed. */
  summary: string;
  /** The summary reports something the launch can't proceed with (no branch picked). */
  warn?: boolean;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}

// A collapsed settings block: header row (label + resolved summary + 変更) and, when
// open, the real controls. Body is unmounted while collapsed — every piece of state
// it edits lives in LaunchModal, so nothing is lost by folding it away.
function LaunchSection({ label, summary, warn = false, open, onToggle, children }: LaunchSectionProps) {
  const tr = useT();
  return (
    <section className={"launch-sec" + (open ? " open" : "")}>
      <button type="button" className="launch-sec-head" aria-expanded={open} onClick={onToggle}>
        <Icon name={open ? "chevron-down" : "chevron-right"} className="launch-sec-chev" />
        <span className="launch-sec-label">{label}</span>
        <span className={"launch-sec-sum" + (warn ? " warn" : "")}>{open ? "" : summary}</span>
        <span className="launch-sec-edit">{tr(open ? "launch.sec_done" : "launch.sec_edit")}</span>
      </button>
      {open && <div className="launch-sec-body">{children}</div>}
    </section>
  );
}

export function LaunchModal({ repo, branch, path, kinds, settling = false, allowWorktree = true, isSvn = false, onClose, onBack, initialPrompt, initialTitle, initialExistingBranch, onLaunch }: LaunchModalProps) {
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
  const [title, setTitle] = useState(initialTitle ?? "");
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
  // 作業ディレクトリ: "" = the working copy root (the usual case). Seeded from what
  // this repo was last launched in, since a monorepo user keeps returning to the same
  // package — the field shows the value, so a remembered folder is never silent.
  const [subdir, setSubdir] = useState(() => resolveSubdir(repo));
  const [base, setBase] = useState(branch || "");
  const [branchName, setBranchName] = useState(""); // "" => derived from the prompt
  const [conflict, setConflict] = useState<"local" | "remote" | "in_use" | null>(null);
  const [conflictWt, setConflictWt] = useState(""); // for "in_use": the copy holding it
  // ブランチ: 新規作成（既定）か、既に存在するブランチをそのまま使うか。後者は
  // worktree に そのブランチを CO するだけで、新しいブランチは作らない（-b なし）。
  const [branchMode, setBranchMode] = useState<"new" | "existing">(initialExistingBranch ? "existing" : "new");
  const [existingBranch, setExistingBranch] = useState(initialExistingBranch || "");
  const [branches, setBranches] = useState<Branch[] | null>(null);
  // Naming: an explicit entry wins as-is (branch == folder); otherwise the server
  // mints a throwaway temp/<slug> in a wip-<slug> folder. The prompt text never
  // feeds the name — deriving words from it produced odd names on mixed-language
  // prompts, so provisional naming is always the slug.
  const explicit = branchName.trim();
  const newBranch = explicit; // "" → server picks temp/<slug>
  const existingMode = worktree && branchMode === "existing";
  // 折りたたみ（launchPrefs）: 前回この端末で開いていたら開いた状態で出す。SCM の
  // 「このブランチで作業」から来たときは、選ばれたブランチが見えていないと不安なので
  // 場所は必ず開く。
  const [placeOpen, setPlaceOpen] = useState(() => !!initialExistingBranch || readLaunchOpen("place"));
  const [advOpen, setAdvOpen] = useState(() => readLaunchOpen("adv"));
  const toggleSection = (key: LaunchSectionKey, open: boolean, set: (v: boolean) => void) => {
    set(!open);
    writeLaunchOpen(key, !open);
  };
  // The worktree folder is named after whichever branch it will end up on.
  const folderSeg = existingMode ? existingBranch : explicit;
  const folder = worktree && folderSeg ? `${repo}@${sanitizeSeg(folderSeg)}` : "";

  // Branch list for 既存ブランチ mode, fetched from the PARENT copy (the worktree is
  // spun off it). Loaded on first entry into the mode and kept — re-picking is common
  // while composing a prompt, and the list is small. Rows already checked out in
  // another worktree arrive flagged (worktree_path) and BranchList disables them:
  // git allows a branch in one working copy at a time, so they are not valid targets.
  useEffect(() => {
    if (!existingMode || branches !== null) return;
    let alive = true;
    api(`api/repos/${encodeURIComponent(repo)}/branches`)
      .then((d) => alive && setBranches(d?.branches || []))
      .catch(() => alive && setBranches([]));
    return () => {
      alive = false;
    };
  }, [existingMode, branches, repo]);

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
  // planMode はチャットの plan トグル用 cap — cursor/copilot/kiro は planMode:false でも
  // tuiStartMode:true（plan 起動対応）なので、起動時モード選択はどちらかの cap で出す。
  const hasStartMode =
    (agentOf(kind).caps.planMode || agentOf(kind).caps.tuiStartMode) &&
    (driverManaged || agentOf(kind).caps.tuiStartMode);
  const canPasteImage = agentOf(kind).caps.imagePaste;
  const effortOptions = useEffortOptions(kind, model);

  // 畳んだセクションの要約 — 開かなくても「何が起きるか」が読み取れる一行。場所は
  // git が実際にやること（新しい作業コピーを作るのか、今のコピーで動かすのか）なので
  // 既定のままでも必ず文章で出す。
  const baseName = base.trim() || branch || "";
  const placeSummary = !worktree
    ? tr("launch.sum.direct", { branch: branch || tr("launch.wc") })
    : existingMode
      ? existingBranch
        ? tr("launch.sum.wt_existing", { branch: existingBranch })
        : tr("launch.sum.wt_existing_none")
      : explicit
        ? tr("launch.sum.wt_named", { branch: explicit, base: baseName || tr("launch.base_default") })
        : tr("launch.sum.wt_auto", { base: baseName || tr("launch.base_default") });
  // 詳細は逆に「既定から動いている項目だけ」を並べる。全部既定なら一言で済ませる —
  // 常に 5 項目書くと、それ自体がまた読まれない灰色の帯になる。
  const advParts = [
    agentOf(kind).managedDriver ? tr(driverManaged ? "launch.sum.driver_managed" : "launch.sum.driver_terminal") : "",
    hasEffort && effort ? tr("launch.sum.effort", { v: effortOptions.find(([v]) => v === effort)?.[1] || effort }) : "",
    hasStartMode && startMode === "plan" ? "Plan" : "",
    title.trim() ? tr("launch.sum.title", { name: title.trim() }) : "",
  ].filter(Boolean);
  const advSummary = advParts.length ? advParts.join(" · ") : tr("launch.sum.defaults");

  // 初期フォーカスは最初のプロンプト欄 — ただしタッチ端末では当てない（フォーカス＝
  // ソフトキーボードが開き、モーダルの大半が隠れる。この抑制は Console 共通の作法で、
  // lib/device.ts coarsePointer を見る）。autoFocus 属性を使わないのも同じ理由で、
  // あちらはブラウザが対象を可視域へスクロールさせ、上に積んだエージェント選択が
  // 開いた瞬間に画面外へ流れる。preventScroll で「先頭が見えたまま、すぐ打てる」。
  useEffect(() => {
    if (!coarsePointer()) textRef.current?.focus({ preventScroll: true });
  }, []);

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

  // start fires the launch. `resolveCollision` is the conflict panel's re-run: the
  // typed name turned out to exist, so check THAT branch out instead of creating it.
  // 既存ブランチ mode arrives here already resolved, with a branch picked from the list.
  const start = async (resolveCollision: boolean) => {
    if (busy) return;
    setBusy(true);
    setConflict(null);
    const useExisting = existingMode || resolveCollision;
    const wtBase = existingMode ? existingBranch : resolveCollision ? newBranch : base.trim();
    const r = await onLaunch({
      kind,
      driver: agentOf(kind).managedDriver ? driver : "",
      model: hasModel ? model : "",
      effort: hasEffort ? effort : "",
      startMode: hasStartMode ? startMode : "normal",
      prompt: prompt.trim(),
      title: title.trim(),
      images: canPasteImage ? images.map((x) => x.file) : [],
      worktree,
      subdir,
      base: worktree ? wtBase : "",
      newBranch: worktree && !useExisting ? newBranch : "",
      useExisting,
    });
    if (r?.ok) {
      onClose();
      return;
    }
    setBusy(false);
    setConflict(r?.conflict ?? null);
    setConflictWt(r?.worktree || "");
    // The conflict panel and its fix button live inside 場所 — a collapsed section
    // would swallow the only explanation for why the launch didn't happen.
    if (r?.conflict) setPlaceOpen(true);
    // The picked branch was taken between listing it and launching — drop the cached
    // list so a reopen of the picker shows who holds it now.
    if (r?.conflict === "in_use") setBranches(null);
  };
  const submit = () => void start(false);
  // 既存ブランチ mode is only launchable once a branch is picked.
  const canLaunch = !!kinds.length && (!existingMode || !!existingBranch);

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
          {/* 折り返しグリッド（ui.css .ui-seg.big）。以前は横スクロールで 4 枚目以降が
              切れており、7 種類あることも気づけなかった。サブラベル（「◯◯ を起動」）は
              カード名の言い換えで情報が無いので title に落とし、1 行ぶん詰めてある。 */}
          <div className="ui-seg big">
            {kinds.map((k) => {
              const a = agentOf(k);
              return (
                <button
                  key={k}
                  type="button"
                  title={tr(a.launchHintKey)}
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

        {/* 最初のプロンプト（任意）— エージェント／モデルの下。初期フォーカスは上の
            useEffect が当てる（autoFocus 属性ではなく preventScroll 付き、かつ
            タッチ端末では当てない）。 */}
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
            placeholder={tr("launch.first_prompt_ph")}
          />
          <span className="ui-field-hint">
            {tr("launch.first_prompt_note")}
            {canPasteImage && " " + tr("launch.image_paste_note")}
          </span>
        </div>

        {/* 場所: worktree（隔離・既定）か このコピーで直接か。worktree 行では選択肢
            なし（この worktree 内で直接起動）ので、折りたたまず 1 行の注記で出す。 */}
        {!allowWorktree ? (
          <>
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
            {/* 作業ディレクトリ: 起動先をリポジトリ配下のフォルダへ絞る（既定は直下）。 */}
            <div className="ui-field">
              <span className="ui-field-label">{tr("launch.field.subdir")}</span>
              <SubdirPicker repo={repo} value={subdir} onChange={setSubdir} />
              <span className="ui-field-hint">{tr("launch.subdir_hint")}</span>
            </div>
          </>
        ) : (
        <LaunchSection
          label={tr("launch.field.location")}
          summary={placeSummary}
          warn={existingMode && !existingBranch}
          open={placeOpen}
          onToggle={() => toggleSection("place", placeOpen, setPlaceOpen)}
        >
            <div className="ui-field">
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
            {/* 作業ディレクトリ: 起動先をリポジトリ配下のフォルダへ絞る（既定は直下）。
                worktree 起動では、新しく作られた worktree 内の同じ相対パスになる。 */}
            <div className="ui-field">
              <span className="ui-field-label">{tr("launch.field.subdir")}</span>
              <SubdirPicker repo={repo} value={subdir} onChange={setSubdir} />
              <span className="ui-field-hint">
                {worktree ? tr("launch.subdir_wt_hint") : tr("launch.subdir_hint")}
              </span>
            </div>
            {worktree && (
              <>
                {/* ブランチ: 新規作成 か、既にあるブランチをそのまま使うか。 */}
                <div className="ui-field">
                  <span className="ui-field-label">{tr("launch.field.branch")}</span>
                  <div className="ui-seg">
                    <button
                      type="button"
                      className={"seg-btn" + (branchMode === "new" ? " active" : "")}
                      onClick={() => {
                        setBranchMode("new");
                        setConflict(null);
                      }}
                    >
                      <Icon name="git-branch" /> {tr("launch.branch_new")}
                      <span className="seg-sub">{tr("launch.branch_new_sub")}</span>
                    </button>
                    <button
                      type="button"
                      className={"seg-btn" + (branchMode === "existing" ? " active" : "")}
                      onClick={() => {
                        setBranchMode("existing");
                        setConflict(null);
                      }}
                    >
                      <Icon name="git-commit" /> {tr("launch.branch_existing")}
                      <span className="seg-sub">{tr("launch.branch_existing_sub")}</span>
                    </button>
                  </div>
                </div>
                {branchMode === "new" ? (
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
                  </>
                ) : (
                  <div className="ui-field">
                    <span className="ui-field-label">{tr("launch.pick_branch")}</span>
                    <BranchList
                      branches={branches}
                      selected={existingBranch}
                      onPick={(name) => {
                        setExistingBranch(name);
                        setConflict(null);
                      }}
                    />
                  </div>
                )}
                <span className="ui-field-hint">
                  {folder ? (
                    <Trans k="launch.wc_created_note" vars={{ folder }} components={[<code />]} />
                  ) : existingMode ? (
                    tr("launch.branch_pick_note")
                  ) : (
                    tr("launch.branch_empty_note")
                  )}
                </span>
                {conflict && (
                  <div className="launch-conflict">
                    {conflict === "in_use" ? (
                      // Not a naming clash: git holds a branch in one working copy at a
                      // time, so the only way forward is that copy (or another branch).
                      <span>
                        <Trans
                          k="launch.branch_in_use"
                          vars={{ branch: existingMode ? existingBranch : newBranch, folder: conflictWt }}
                          components={[<code />, <code />]}
                        />
                      </span>
                    ) : conflict === "local" ? (
                      <span>
                        <Trans k="launch.local_branch_exists" vars={{ branch: newBranch }} components={[<code />]} />
                      </span>
                    ) : (
                      <span>
                        <Trans k="launch.remote_branch_exists" vars={{ branch: newBranch }} components={[<code />]} />
                      </span>
                    )}
                    {/* Both collisions resolve the same way — check the existing branch
                        out instead of forking a divergent one. A LOCAL clash used to
                        dead-end here even though the worktree path handles it fine. */}
                    {(conflict === "local" || conflict === "remote") && (
                      <Button small icon="git-branch" disabled={busy} onClick={() => void start(true)}>
                        {tr("launch.work_existing_branch")}
                      </Button>
                    )}
                  </div>
                )}
              </>
            )}
            </div>
        </LaunchSection>
        )}

        {/* 詳細: 一度決めたらしばらく変えない設定（実行方式・effort・開始モード・
            セッション名）。既定から動いている項目だけが要約行に出る。 */}
        <LaunchSection
          label={tr("launch.field.advanced")}
          summary={advSummary}
          open={advOpen}
          onToggle={() => toggleSection("adv", advOpen, setAdvOpen)}
        >
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

          <div className="ui-field">
            <span className="ui-field-label">{tr("launch.field.title")}</span>
            <input value={title} maxLength={512} onChange={(e) => setTitle(e.target.value)} placeholder={tr("launch.title_ph")} />
          </div>
        </LaunchSection>
      </div>

      <footer className="ui-modal-foot">
        {onBack && (
          <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={onBack} disabled={busy}>
            {tr("launch.back_to_hub")}
          </Button>
        )}
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" onClick={submit} disabled={busy || !canLaunch}>
          {busy ? tr("launch.launching") : worktree ? tr("launch.start_worktree") : tr("launch.launch")}
        </Button>
      </footer>
    </Modal>
  );
}
