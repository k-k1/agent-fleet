// transcript/blocks — the individual renderings a conversation block is built from.
//
// Every component here is presentational: it takes data plus (optionally) a callback,
// and reaches for no session-scoped API of its own. The three places that used to call
// owner-only endpoints directly now take them as parameters instead —
//
//   ErrorBlock    onReauth          (was useSettingsUI.openSettings)
//   PastedThumb   loadPastedImage   (was raw(api/sessions/{name}/pasted/…))
//   FileThumb     fileURL           (was downloadURL)
//
// — which is what lets the shared-session view mount the same blocks without a route to
// somebody else's Workspace. See capabilities.ts for the rule about absent callbacks.

import { useEffect, useRef, useState } from "react";
import type { ReactNode, RefObject } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import FileIcon from "../../../ui/FileIcon.tsx";
import { baseName, imageFormat } from "../../../lib/filemeta.ts";
import { fmtDateTime, fmtNum } from "../../../lib/intl.ts";
import { fmtTok } from "../../../lib/fmttok.ts";
import { prettyModel } from "../../../lib/modelName.ts";
import { t as tr, tCount } from "../../../lib/i18n/index.ts";
import { useSettings } from "../../../lib/settings.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { lineDiff, type DiffEdit } from "../../viewer/DiffView.tsx";
import { previewBody } from "../optionPreview.ts";
import { parseQuestionAnswers, resolveAnswer } from "../questionAnswers.ts";
import { planOutcome } from "../planDecision.ts";
import { planKey, removePlanComment, unsentComments, usePlanComments } from "../planComments.ts";
import type { Group, Part, Question, QuestionOption, TaskItem, TurnTtsWiring } from "./types.ts";

// formatTS renders an RFC3339 timestamp as local "MM/DD HH:MM" (date kept so a long
// session that spans days stays unambiguous).
export const formatTS = (iso: string) => fmtDateTime(iso);

// prettyCwd collapses the home prefix to ~ so the working dir reads compactly.
export function prettyCwd(p: string) {
  return p.replace(/^\/home\/[^/]+/, "~");
}

// taskIcon maps a ToDo status to its codicon glyph (in_progress spins via the caller).
function taskIcon(status: string): string {
  if (status === "completed") return "check";
  if (status === "in_progress") return "loading";
  return "circle-large-outline";
}

// Small localStorage accessors for the per-session UI state of the strips that sit under
// the mirror's head (the ToDo panel's open/dismissed, the changed-files panel's open).
// Errors (private mode, quota) are swallowed — the state just won't persist. Mirrors the
// swallow-and-continue pattern in lib/draft.ts.
export function readLS(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
export function writeLS(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable — state just won't persist */
  }
}

// Keep disclosure content mounted while it is closed so CSS can animate both directions.
// `inert` preserves the native <details> behaviour: hidden controls and links cannot receive
// focus (or be exposed to assistive technology) until the disclosure is opened again.
export function DisclosureContent({ open, className, children }: { open: boolean; className: string; children: ReactNode }) {
  return (
    <div className="mirror-disclosure-panel" aria-hidden={!open}>
      <div className="mirror-disclosure-inner" inert={!open}>
        <div className={className}>{children}</div>
      </div>
    </div>
  );
}

// TaskChecklist renders the current ToDo list (reconstructed from Task tool calls) as a
// collapsed disclosure: a done/total count, the active task on the summary, and the full
// list on expand. The open/closed choice is remembered per session (localStorage), so it
// survives ターミナル⇄チャット swaps and reloads; without a stored choice it defaults open
// while work remains. A ✕ dismisses an abandoned list — the dismissal is keyed to the
// current task set, so a newly-started ToDo (different task ids) re-appears on its own.
// The parent re-keys this component by session, so switching sessions re-reads the right
// per-session state. (See taskIcon: in_progress spins, visible in the summary too so the
// running task's ぐるぐる shows even while collapsed.)
export function TaskChecklist({ tasks, session }: { tasks: TaskItem[]; session: string }) {
  const done = tasks.filter((t) => t.status === "completed").length;
  const total = tasks.length;
  const active = tasks.find((t) => t.status === "in_progress");
  // A signature of the current task set — dismissing stores it, and the panel stays hidden
  // only while it matches (a new list has different ids → shows again).
  const sig = tasks.map((t) => t.id).join("|");
  const openKey = "af.mirror-todo-open." + session;
  const dismissKey = "af.mirror-todo-dismiss." + session;

  const [dismissedSig, setDismissedSig] = useState<string | null>(() => readLS(dismissKey));
  // Stored per-session choice wins; with none, open while work remains.
  const [open, setOpen] = useState(() => {
    const v = readLS(openKey);
    return v === null ? done < total : v === "1";
  });

  if (dismissedSig === sig) return null; // this exact list was dismissed

  return (
    <section className={"mirror-tasks mirror-disclosure" + (open ? " open" : "")}>
      <div className="mirror-tasks-head">
        <button
          type="button"
          className="mirror-tasks-toggle"
          aria-expanded={open}
          onClick={() => {
            const next = !open;
            setOpen(next);
            writeLS(openKey, next ? "1" : "0");
          }}
        >
          <Icon name="checklist" />
          <span className="mtk-title">ToDo</span>
          <span className="mtk-count muted">
            {done}/{total}
          </span>
          {active && (
            <span className="mtk-active muted">
              {/* Spinner rides the summary so the ぐるぐる stays visible even
                  while the list is collapsed. */}
              <Icon name="loading" spin className="mtk-active-mark" />
              {active.activeForm || active.subject}
            </span>
          )}
        </button>
        <button
          type="button"
          className="mtk-dismiss"
          title={tr("mirror.todo_dismiss")}
          onClick={() => {
            setDismissedSig(sig);
            writeLS(dismissKey, sig);
          }}
        >
          <Icon name="close" />
        </button>
      </div>
      <DisclosureContent open={open} className="mirror-tasks-list-wrap">
        <ol className="mirror-tasks-list">
          {tasks.map((t) => {
            const label = t.status === "in_progress" && t.activeForm ? t.activeForm : t.subject;
            return (
              <li key={t.id} className={"mtk-item mtk-" + t.status}>
                <Icon name={taskIcon(t.status)} spin={t.status === "in_progress"} className="mtk-mark" />
                <span className="mtk-text" title={label}>
                  {label}
                </span>
              </li>
            );
          })}
        </ol>
      </DisclosureContent>
    </section>
  );
}

// CompactBlock renders claude's auto-compaction summary as a collapsed disclosure —
// "コンテキストが圧縮されました" — rather than a giant user turn. Closed by default
// (native <details>); expand to read the summary that replaced the earlier context.
export function CompactBlock({
  turn,
  before,
  after,
  repo,
  onOpenFile,
}: {
  turn: Group;
  before?: number;
  after?: number;
  repo?: string | null;
  onOpenFile?: (path: string, line?: number, column?: number) => void;
}) {
  // Show the reduction only once both sides are real: `after` is 0 until the first
  // post-compaction turn's usage lands, so the effect appears a beat after 圧縮完了.
  const hasEffect = !!before && !!after && before > after;
  const cut = hasEffect ? before! - after! : 0;
  const pct = hasEffect ? Math.round((cut / before!) * 100) : 0;
  return (
    <details className="mirror-compact">
      <summary className="mirror-compact-head">
        <Icon name="archive" />
        <span className="mc-title">{tr("mirror.context_compacted")}</span>
        {hasEffect && (
          <span className="mc-effect" title={tr("mirror.token_change", { before: fmtNum(before!), after: fmtNum(after!) })}>
            {fmtTok(before!)} → {fmtTok(after!)}
            <span className="mc-effect-pct">−{pct}%</span>
          </span>
        )}
        {turn.ts && <span className="mc-time muted">{formatTS(turn.ts)}</span>}
      </summary>
      <div className="mirror-compact-body">
        {hasEffect && (
          <div className="mc-bars" aria-hidden="true">
            <div className="mc-bar-row">
              <span className="mc-bar-lbl">{tr("mirror.before_compact")}</span>
              <span className="mc-bar-track">
                <span className="mc-bar-fill before" style={{ width: "100%" }} />
              </span>
              <span className="mc-bar-val">{fmtTok(before!)}</span>
            </div>
            <div className="mc-bar-row">
              <span className="mc-bar-lbl">{tr("mirror.after_compact")}</span>
              <span className="mc-bar-track">
                <span className="mc-bar-fill after" style={{ width: Math.max(2, (after! / before!) * 100) + "%" }} />
              </span>
              <span className="mc-bar-val">{fmtTok(after!)}</span>
            </div>
          </div>
        )}
        <MarkdownView source={turn.text} baseDir={turn.cwd} repo={repo} onOpenFile={onOpenFile} />
      </div>
    </details>
  );
}

// ThinkingBlock renders an agent's chain-of-thought (codex/opencode reasoning) as a
// disclosure — "思考" — so it's available without crowding the answer. Collapsed unless
// defaultOpen: the per-kind 設定 > エージェント >（各カード）動作設定「思考を展開して表示」
// （既定オフ＝従来どおり畳む）。WorkDisclosure と同じく開閉状態はローカルに持つので、
// クリックで畳んだ／開いた結果は再描画で巻き戻らない。設定を切り替えたときだけ、開いて
// いるミラーにも即座に反映されるよう defaultOpen へ再同期する。
export function ThinkingBlock({
  text,
  defaultOpen,
  baseDir,
  repo,
  onOpenFile,
}: {
  text?: string;
  defaultOpen: boolean;
  baseDir?: string;
  repo?: string | null;
  onOpenFile?: (path: string, line?: number, column?: number) => void;
}) {
  const [open, setOpen] = useState(defaultOpen);
  useEffect(() => {
    setOpen(defaultOpen);
  }, [defaultOpen]);
  if (!text) return null;
  return (
    <section className={"mirror-thinking mirror-disclosure" + (open ? " open" : "")}>
      <button type="button" className="mirror-thinking-head" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <Icon name="lightbulb" />
        <span className="mth-title">{tr("mirror.thinking_label")}</span>
      </button>
      <DisclosureContent open={open} className="mirror-thinking-body">
        <MarkdownView source={text} baseDir={baseDir} repo={repo} onOpenFile={onOpenFile} />
      </DisclosureContent>
    </section>
  );
}

// ErrorBlock renders a turn that ended in a provider-side error instead of an answer
// (part kind "error"): the agent recorded a failure — an expired login, an exhausted
// balance, a rate limit — and produced no output. Always expanded and visually distinct:
// this used to be invisible, so the session just went quiet and read 入力待ち again.
// The message is provider text, not Markdown, so it renders verbatim.
//
// When the agent classified the failure as「サインインし直せば直る」(cause="auth") the
// raw text alone is a dead end: それは CLI 向けの文面（"Please run /login"）で、Console
// から見ている利用者に効く操作ではない。原文は証拠として残したまま、何が起きたのかと
// どこで直すのかを添える。onReauth 無し（共有ビュー）ではその導線を出さない — 他人の
// エージェントに受信者がサインインし直すことはできないので、案内した先が行き止まりになる。
export function ErrorBlock({
  info,
  text,
  cause,
  agentName,
  onReauth,
}: {
  info?: string;
  text?: string;
  cause?: string;
  agentName: string;
  onReauth?: () => void;
}) {
  if (!text && !info) return null;
  return (
    <div className="mirror-error" role="alert">
      <div className="mirror-error-head">
        <Icon name="error" />
        <span className="mte-title">{tr("mirror.error_label")}</span>
        {info && <span className="mte-code">{info}</span>}
      </div>
      {cause === "auth" && onReauth && (
        <div className="mirror-error-fix">
          <p className="mef-msg">{tr("mirror.error_auth_hint", { agent: agentName })}</p>
          <button type="button" className="mef-action" onClick={onReauth}>
            <Icon name="plug" />
            {tr("mirror.error_auth_action")}
          </button>
        </div>
      )}
      {text && <div className="mirror-error-body">{text}</div>}
    </div>
  );
}

// stripAnsi removes SGR color/style escape sequences so shell output renders as plain text.
function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*m/g, "");
}

// BashBlock renders a `!`-run shell command as a terminal block: the command line always
// shown ("$ cmd"), its stdout/stderr collapsed by default behind a "出力 (N 行)" toggle
// (matching the tool-run disclosure). stderr is tinted; an empty result shows no toggle.
export function BashBlock({ command, stdout, stderr }: { command?: string; stdout?: string; stderr?: string }) {
  const [open, setOpen] = useState(false);
  const out = stripAnsi(stdout || "").replace(/\s+$/, "");
  const err = stripAnsi(stderr || "").replace(/\s+$/, "");
  const hasOut = !!(out || err);
  const lines = (out ? out.split("\n").length : 0) + (err ? err.split("\n").length : 0);
  return (
    <div className={"mt-bash" + (open ? " open" : "")}>
      <div className="mt-bash-cmd">
        <span className="mt-bash-prompt">$</span>
        <code className="mt-bash-code">{command}</code>
      </div>
      {hasOut && (
        <>
          <button
            type="button"
            className="mt-bash-toggle"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            title={open ? tr("mirror.collapse_output") : tr("mirror.show_output")}
          >
            <Icon name={open ? "chevron-down" : "chevron-right"} />
            <span>{tr("mirror.output_lines", { lines })}</span>
          </button>
          {open && (
            <pre className="mt-bash-output">
              {out}
              {err && <span className="mt-bash-err">{(out ? "\n" : "") + err}</span>}
            </pre>
          )}
        </>
      )}
    </div>
  );
}

// CmdChip renders a `/`-run slash command / skill invocation as a compact chip — the
// command name with its arguments — marking that the user triggered it. The command's
// effect (a skill's work) shows as the following turns; builtins' terminal feedback stays hidden.
export function CmdChip({ name, args }: { name?: string; args?: string }) {
  return (
    <div className="mt-cmd">
      <Icon name="play" />
      <code className="mt-cmd-name">{name}</code>
      {args && <span className="mt-cmd-args">{args}</span>}
    </div>
  );
}

// ContextLine marks the git branch / working dir in effect from here on.
export function ContextLine({ branch, cwd }: { branch?: string; cwd?: string }) {
  return (
    <div className="mirror-context">
      {branch && (
        <span className="mc-branch">
          <Icon name="git-branch" /> {branch}
        </span>
      )}
      {cwd && <span className="mc-cwd">{prettyCwd(cwd)}</span>}
    </div>
  );
}

// WorkDisclosure appears only once a response is complete and a final text exists after
// tool activity. It therefore mounts at the completion boundary: users following the tail
// get a closed summary, while someone who scrolled up to read the process keeps it open.
//
// Deliberately CONTROLLED (open/onToggle) rather than holding its own state off a
// defaultOpen: the disclosure comes and goes with workSplit — a tool arriving after the
// final text moves the boundary and makes the split vanish for a poll or two — and local
// state would be destroyed on every such unmount, re-deciding the fold from whatever the
// follow flag happens to be. The owner (TranscriptTurn) outlives that and keeps the choice.
export function WorkDisclosure({
  tools,
  responses,
  open,
  onToggle,
  children,
}: {
  tools: number;
  responses: number;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  return (
    <section className={"mt-work mirror-disclosure" + (open ? " open" : "")}>
      <button type="button" className="mt-work-head" aria-expanded={open} onClick={onToggle}>
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="mt-work-title">{tr("chat.work_process")}</span>
        <span className="mt-work-count muted">
          {tCount("chat.tool_count", tools)}
          {responses > 0 ? tCount("chat.interim_count", responses) : ""}
        </span>
      </button>
      <DisclosureContent open={open} className="mt-work-body">{children}</DisclosureContent>
    </section>
  );
}

// DelegationCard keeps orchestration visible without dumping the child agent's private
// working transcript into the main conversation. The full instruction and final result
// (when the provider records one) remain available on demand.
export function DelegationCard({ p, agentName }: { p: Part; agentName: string }) {
  const status = p.status || "requested";
  const statusLabel: Record<string, string> = {
    requested: tr("mirror.task.requested"),
    running: tr("mirror.task.running"),
    completed: tr("mirror.task.completed"),
    failed: tr("mirror.task.failed"),
  };
  const detail = !!(p.prompt || p.output);
  return (
    <div className="mt-delegation">
      <div className="mt-delegation-head">
        <Icon name="repo-forked" />
        <span className="mt-delegation-title">{tr("mirror.delegation_title", { name: agentName })}</span>
        <span className={"mt-delegation-status " + status}>{statusLabel[status] || status}</span>
      </div>
      {(p.info || p.agentType || p.model) && (
        <div className="mt-delegation-meta">
          {p.info && <span className="mt-delegation-label">{p.info}</span>}
          {p.agentType && p.agentType !== p.info && <code>{p.agentType}</code>}
          {p.model && <code>{prettyModel(p.model)}</code>}
        </div>
      )}
      {detail && (
        <details className="mt-delegation-detail">
          <summary>{tr("mirror.detail")}</summary>
          {p.prompt && (
            <div className="mt-delegation-section">
              <div className="muted">{tr("mirror.delegated_prompt")}</div>
              <pre>{p.prompt}</pre>
            </div>
          )}
          {p.output && (
            <div className="mt-delegation-section">
              <div className="muted">{tr("mirror.result")}</div>
              <pre>{p.output}</pre>
            </div>
          )}
        </details>
      )}
    </div>
  );
}

// TurnTtsButtons — ターンフッターの読み上げ操作（カラオケ朗読）。待機中は「読み上げ」1 つ、
// このターンを読み上げ中は「一時停止/再開・停止」に切り替わる（ReaderView ヘッダと同構成）。
// ラベルは付けずアイコンのみ（ペインが狭いとフッターの並びが崩れるため。意味は title で）。
// ttsEnabled かつ本文があるときだけ表示。ChatView の TtsReadButton（ストリーム型 speakText）
// とは別物で、ミラーは完結ターンを朗読するのでハイライト・途中再開が付く。
export function TurnTtsButtons({
  turn,
  tts,
  body,
}: {
  turn: Group;
  tts: TurnTtsWiring;
  body: RefObject<HTMLDivElement | null>;
}) {
  const enabled = useSettings().ttsEnabled;
  if (!enabled || turn.idx === undefined || !(turn.text || "").trim()) return null;
  const mine = tts.reading?.idx === turn.idx;
  if (!mine) {
    return (
      <button
        type="button"
        className="ghost mt-copy"
        title={tr("mirror.read_turn")}
        onClick={() => body.current && tts.start(turn.idx!, body.current)}
      >
        <Icon name="unmute" />
      </button>
    );
  }
  const paused = tts.reading!.paused;
  return (
    <>
      <button
        type="button"
        className="ghost mt-copy"
        title={paused ? tr("mirror.tts_resume") : tr("mirror.tts_pause")}
        onClick={paused ? tts.resume : tts.pause}
      >
        <Icon name={paused ? "play" : "debug-pause"} />
      </button>
      <button type="button" className="ghost mt-copy" title={tr("mirror.tts_stop")} onClick={tts.stop}>
        <Icon name="debug-stop" />
      </button>
    </>
  );
}

// TurnSpendBar visualizes one turn's newly-consumed tokens as a stacked bar, scaled so
// the heaviest turn in the conversation fills the track — the bar length shows the turn's
// relative weight, the segments its input(uncached)/cache-creation/output split. Cache
// reads (reused context) are excluded; the ↑ number beside it carries total context.
export function TurnSpendBar({ fresh, create, out, max }: { fresh: number; create: number; out: number; max: number }) {
  const pct = (n: number) => (n / max) * 100 + "%";
  const total = fresh + create + out;
  const title =
    tr("mirror.turn_tokens", { total: fmtNum(total) }) +
    "\n" +
    tr("mirror.turn_tokens_detail", {
      fresh: fmtNum(fresh),
      create: fmtNum(create),
      out: fmtNum(out),
    });
  return (
    <span className="mt-spend" title={title} aria-hidden="true">
      <span className="ts-seg ts-fresh" style={{ width: pct(fresh) }} />
      <span className="ts-seg ts-create" style={{ width: pct(create) }} />
      <span className="ts-seg ts-out" style={{ width: pct(out) }} />
    </span>
  );
}

// PastedThumb shows a small preview of a pasted image referenced in a turn. The bytes
// arrive through the caller's loader (the mirror's endpoint needs an auth header, so an
// <img src> can't fetch it directly) and become an object URL, handed to the lightbox on
// click. A view with no loader never mounts this — see TranscriptTurn.
export function PastedThumb({
  name,
  load,
  onOpen,
}: {
  name: string;
  load: (name: string) => Promise<Blob | null>;
  onOpen?: (url: string) => void;
}) {
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let alive = true;
    let obj = "";
    load(name)
      .then((b) => {
        if (!alive) return;
        if (!b) {
          setFailed(true);
          return;
        }
        obj = URL.createObjectURL(b);
        setUrl(obj);
      })
      .catch(() => {
        if (alive) setFailed(true);
      });
    return () => {
      alive = false;
      if (obj) URL.revokeObjectURL(obj);
    };
  }, [name, load]);
  if (failed) {
    return (
      <span className="mt-img mt-img-loading" title={tr("chat.preview_failed")}>
        <Icon name="file-media" />
      </span>
    );
  }
  if (!url) {
    return (
      <span className="mt-img mt-img-loading">
        <Icon name="loading" spin />
      </span>
    );
  }
  return (
    <button type="button" className="mt-img" title={tr("chat.click_to_zoom")} onClick={() => onOpen && onOpen(url)}>
      <img src={url} alt={tr("chat.pasted_image_alt")} />
    </button>
  );
}

// diffStat renders each captured edit through the SAME line differ the diff pane uses
// (viewer/DiffView lineDiff), so an inline expansion and an opened pane can never
// disagree about what changed. Returns the per-edit rows plus the aggregate +/- counts.
function diffStat(edits: DiffEdit[]) {
  let added = 0;
  let removed = 0;
  const hunks = edits.map((e) => {
    const rows = lineDiff(e.old || "", e.new || "");
    for (const r of rows) {
      if (r.t === "add") added++;
      else if (r.t === "del") removed++;
    }
    return rows;
  });
  return { hunks, added, removed };
}

// InlineEdits shows an edit's before/after right where the tool trace sits, for views
// that have no diff pane to open. The shared-session DTO (docs/59 §3) keeps the diff
// BODY (old/new) but drops the file path, so there is no coordinate to open — the
// change itself is all there is to show, and showing it here is the whole affordance.
// Reuses the diff pane's dv-* markup (viewer.css is loaded globally) so the two read
// identically.
export function InlineEdits({ edits }: { edits: DiffEdit[] }) {
  const { hunks } = diffStat(edits);
  return (
    <div className="mt-tool-diff-inline">
      {hunks.map((rows, hi) => (
        <div className="dv-hunk" key={hi}>
          {hunks.length > 1 && <div className="dv-hunk-head">{tr("view.change")} {hi + 1}</div>}
          <table className="dv-table">
            <tbody>
              {rows.map((r, i) => (
                <tr className={"dv-row dv-" + r.t} key={i}>
                  <td className="dv-gutter">{r.o || ""}</td>
                  <td className="dv-gutter">{r.n || ""}</td>
                  <td className="dv-mark">{r.t === "add" ? "+" : r.t === "del" ? "−" : ""}</td>
                  <td className="dv-code">{r.text}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  );
}

// ToolTrace renders one faint tool line. Edit-family tools carry their before/after,
// so they render as a button that opens a diff pane — or, when this view has no pane to
// open one in, as a click-to-expand inline diff; a tool that carries its output
// (codex/opencode) becomes a click-to-expand row showing the result; the rest are a
// static trace.
export function ToolTrace({ p, onOpenDiff }: { p: Part; onOpenDiff?: (p: Part) => void }) {
  const [open, setOpen] = useState(false);
  if (p.edits && p.edits.length) {
    if (onOpenDiff) {
      return (
        <button
          type="button"
          className="mt-tool mt-tool-diff"
          onClick={() => onOpenDiff(p)}
          title={tr("mirror.open_diff")}
        >
          <Icon name="diff" />
          <span className="mt-tool-name">{p.tool}</span>
          {p.info && <span className="mt-tool-info">{p.info}</span>}
        </button>
      );
    }
    const { added, removed } = diffStat(p.edits);
    return (
      <div className={"mt-tool-out" + (open ? " open" : "")}>
        <button
          type="button"
          className="mt-tool mt-tool-outhead"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          title={open ? tr("mirror.collapse_changes") : tr("mirror.show_changes")}
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} />
          <span className="mt-tool-name">{p.tool}</span>
          {p.info && <span className="mt-tool-info">{p.info}</span>}
          <span className="mt-tool-diffstat">
            {added > 0 && <span className="dv-add">+{added}</span>}
            {removed > 0 && <span className="dv-del">−{removed}</span>}
          </span>
        </button>
        {open && <InlineEdits edits={p.edits} />}
      </div>
    );
  }
  if (p.output) {
    return (
      <div className={"mt-tool-out" + (open ? " open" : "")}>
        <button
          type="button"
          className="mt-tool mt-tool-outhead"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          title={open ? tr("mirror.collapse_output") : tr("mirror.show_output")}
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} />
          <span className="mt-tool-name">{p.tool}</span>
          {p.info && <span className="mt-tool-info">{p.info}</span>}
        </button>
        {open && <pre className="mt-tool-output">{p.output}</pre>}
      </div>
    );
  }
  return (
    <div className="mt-tool">
      <Icon name="tools" />
      <span className="mt-tool-name">{p.tool}</span>
      {p.info && <span className="mt-tool-info">{p.info}</span>}
    </div>
  );
}

// ToolRun renders a run of consecutive tool traces. A lone tool shows inline as
// before; two or more collapse (default) into a summary row — "N 件のツール" with a
// per-tool tally (Edit×3 · Bash×2) — that expands on click to the individual traces,
// keeping each edit's click-to-diff.
export function ToolRun({ tools, onOpenDiff }: { tools: { p: Part; i: number }[]; onOpenDiff?: (p: Part) => void }) {
  const [open, setOpen] = useState(false);
  if (tools.length === 1) return <ToolTrace p={tools[0].p} onOpenDiff={onOpenDiff} />;
  const tally: [string, number][] = [];
  const at: Record<string, number> = {};
  for (const { p } of tools) {
    const name = p.tool || "tool";
    if (at[name] === undefined) {
      at[name] = tally.length;
      tally.push([name, 0]);
    }
    tally[at[name]][1]++;
  }
  const summary = tally.map(([n, c]) => (c > 1 ? `${n}×${c}` : n)).join(" · ");
  return (
    <div className={"mt-toolrun" + (open ? " open" : "")}>
      <button
        type="button"
        className="mt-tool mt-toolrun-head"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={open ? tr("mirror.collapse_tools") : tr("mirror.expand_tools")}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="mt-tool-name">{tCount("mirror.tools_count", tools.length)}</span>
        <span className="mt-tool-info">{summary}</span>
      </button>
      {open && (
        <div className="mt-toolrun-body">
          {tools.map(({ p, i }) => (
            <ToolTrace key={i} p={p} onOpenDiff={onOpenDiff} />
          ))}
        </div>
      )}
    </div>
  );
}

// One option's body — label, description, and (when the agent attached one) its preview.
// Shared by the pending form and the answered card so a preview can never show up in one
// and not the other. Everything is a <span>: the body lives inside a <button>, which may
// only contain phrasing content, so a <pre> here would be invalid markup.
export function OptionBody({ o }: { o: QuestionOption }) {
  const preview = previewBody(o.preview);
  return (
    <span className="mq-opt-body">
      <span className="mq-opt-label">{o.label}</span>
      {o.description && <span className="mq-opt-desc">{o.description}</span>}
      {preview && <span className="mq-opt-preview">{preview}</span>}
    </span>
  );
}

// Does any option carry a preview? Mockups need the card's full width, so the options
// then stack in ONE column instead of the usual ~220px auto-fit grid.
export const hasPreview = (qs: Question[]) => qs.some((q) => (q.options || []).some((o) => previewBody(o.preview) !== ""));

// QuestionBlock renders an AskUserQuestion from the transcript: header + prompt +
// options, inert, with the chosen option highlighted once `answered` says the answer is
// here. It is NOT answered merely by being in the transcript — claude writes the tool_use
// at ASK time, so an open (or abandoned) question can be in there too.
export function QuestionBlock({
  questions,
  answered,
  answer,
  declined,
}: {
  questions?: Question[];
  answered?: boolean;
  answer?: string;
  // declined: the tool_result was claude's own decline boilerplate (an Escape out of
  // the modal — e.g. docs/dev/92 §6's preview free-text bug), not a genuine answer.
  // Rendering `answer` as if it were a pick would parse to nothing but still badge
  // "回答済み" — the exact "answered but not recognized" confusion this fixes.
  declined?: boolean;
}) {
  const norm = (answer || "").trim();
  // Per-question answers, so each card shows its OWN reply instead of the whole raw
  // tool_result. Parsing (including the quotes a user may type inside a free-text
  // answer) and the pick/free-text split live in questionAnswers.ts.
  const qs = questions || [];
  const pairs = parseQuestionAnswers(norm, qs.map((q) => q.question));
  const answerAt = (qi: number) => (pairs.length ? pairs[qi] || "" : norm);
  const wide = hasPreview(qs);
  return (
    <div className={"mt-question" + (answered ? " answered" : "") + (declined ? " declined" : "")}>
      {qs.map((qn, qi) => {
        const opts = qn.options || [];
        const a = answerAt(qi);
        // extras is the custom "Type something" entry, which multi-select can COMBINE
        // with checked options — show it even when an option also matched, or the typed
        // text silently vanishes from the answered card. Skipped entirely when declined:
        // `a` is claude's rejection prose, not a pick, and parsing it as one would show
        // whatever fragment happened to match as if the user had typed it.
        const { chosen, extras } =
          answered && !declined
            ? resolveAnswer(a, opts.map((o) => o.label))
            : { chosen: [] as string[], extras: [] as string[] };
        const chosenSet = new Set(chosen);
        return (
          <div className="mq" key={qi}>
            <div className="mq-head">
              <Icon name="comment-discussion" />
              {qn.header && <span className="mq-header">{qn.header}</span>}
              {qn.multiSelect && <span className="mq-multi muted">{tr("mirror.multi_select_ok")}</span>}
              {answered && (
                <span className={"mq-done muted" + (declined ? " declined" : "")}>
                  {declined ? tr("mirror.rejected") : tr("mirror.answered")}
                </span>
              )}
            </div>
            {qn.question && <div className="mq-text">{qn.question}</div>}
            <div className={"mq-options" + (wide ? " wide" : "")}>
              {opts.map((o, oi) => {
                const sel = chosenSet.has(o.label);
                return (
                  <button
                    type="button"
                    className={"mq-opt" + (sel ? " selected" : "")}
                    key={oi}
                    disabled
                    title={o.description || o.label}
                  >
                    <span className="mq-mark">{qn.multiSelect ? (sel ? "☑" : "☐") : sel ? "◉" : "○"}</span>
                    <OptionBody o={o} />
                  </button>
                );
              })}
            </div>
            {answered && declined && qi === 0 && (
              // One note for the whole card (not per question) — claude declines the
              // WHOLE AskUserQuestion call, not individual questions within it.
              <div className="mq-answer mq-declined-note muted">{tr("mirror.question_declined")}</div>
            )}
            {answered && !declined && extras.length > 0 && (
              // A free-text ("Type something") reply is the user's actual words — surface it as
              // an accented callout, not a muted footnote, so it doesn't get lost next to the
              // (possibly none) highlighted options.
              <div className="mq-answer mq-free">
                <span className="mq-free-tag">{chosenSet.size ? tr("mirror.freeform_label") : tr("mirror.answer_label")}</span>
                <span className="mq-free-text">{extras.join(tr("common.list_sep"))}</span>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

// planTitle / planSummary derive a compact heading + lead line from the plan Markdown.
export function planTitle(md?: string) {
  const m = (md || "").match(/^#{1,3}\s+(.+)$/m);
  return m ? m[1].trim() : tr("mirror.plan_fallback");
}
export function planSummary(md?: string) {
  for (const line of (md || "").split("\n")) {
    const s = line.trim();
    if (s && !s.startsWith("#") && !s.startsWith("```")) {
      return s.length > 100 ? s.slice(0, 100) + "…" : s;
    }
  }
  return "";
}

// PlanBlock shows an ExitPlanMode plan compactly (title + one-line summary) with a
// button to open the full Markdown in its own pane, and — while pending — an approve
// button that confirms the plan (Enter = "Yes, and bypass permissions").
export function PlanBlock({
  plan,
  session,
  pending,
  answered,
  outcome,
  forceRejected,
  onOpen,
  onApprove,
  onReject,
  onSendComments,
  sendDisabled,
  sending,
}: {
  plan?: string;
  /** コメントの束を引く鍵に使う（レビュー面と同じ planKey）。 */
  session?: string;
  pending?: boolean;
  answered?: boolean;
  outcome?: string;
  forceRejected?: boolean;
  onOpen?: () => void;
  onApprove?: () => void;
  onReject?: () => void;
  onSendComments?: () => void;
  /** 送信できない理由（"" / 未指定 = 送れる）。停止中セッションでボタンを塞ぐ。 */
  sendDisabled?: string;
  sending?: boolean;
}) {
  // A plan in the transcript was presented and resolved — classify its outcome text
  // (best-effort; the exact result text varies). planOutcome reconciles the optimistic
  // 却下 mark (forceRejected) against the real tool_result: a definitive approval wins so
  // a card can't stay badged 却下 while claude coded, an interrupt result badges 却下, and
  // an empty/unknown outcome (the result can lag a poll or two) stays 決定済み. See
  // planDecision.ts.
  const kind = planOutcome(outcome, !!forceRejected);
  const approved = kind === "approved";
  const rejected = kind === "rejected";
  // 決着のバッジを出す条件。answered = tool_result が来た（＝転写に出ているだけでは
  // 決着ではない。claude は ASK 時点で tool_use を書く）。楽観 却下 マークはそれより
  // 先に付くので、承認待ちでない限りこれも決着として数える — でないと押した瞬間に
  // バッジが消え、tool_result が来るまで宙に浮く。
  const decided = !!answered || (!pending && !!forceRejected);
  // レビュー面（doc ペイン）で溜めたコメント。承認待ちに限らず引く — 却下したあとでも
  // 追加の指摘を送れるようにするため（plan モードのまま入力待ちに戻るので、そのときは
  // 普通の発話として届く）。
  const commentKey = session && plan ? planKey(session, plan) : null;
  const comments = usePlanComments(commentKey);
  const unsent = unsentComments(comments);
  // No pane to open the plan into (shared-session view) — expand it in place instead, so
  // the proposal is still readable in full rather than reduced to its title + lead line.
  const [expanded, setExpanded] = useState(false);
  const inline = !onOpen && !!plan;
  return (
    <div className={"mt-plan" + (decided ? " decided" : "")}>
      <div className="mt-plan-head">
        <Icon name="checklist" />
        <span className="mt-plan-title">{planTitle(plan)}</span>
        {pending && <span className="mt-plan-badge">{tr("mirror.approval_pending")}</span>}
        {decided && (
          <span className={"mt-plan-badge" + (approved ? " ok" : rejected ? " no" : "")}>
            {approved ? tr("mirror.approved") : rejected ? tr("mirror.rejected") : tr("mirror.decided")}
          </span>
        )}
      </div>
      {planSummary(plan) && <div className="mt-plan-summary">{planSummary(plan)}</div>}
      {comments.length > 0 && (
        <ol className="mt-plan-comments">
          {comments.map((c, i) => (
            <li key={c.id} className={"mt-plan-comment" + (c.sentAt ? " sent" : "")}>
              <span className="mt-plan-comment-n">{i + 1}</span>
              <span className="mt-plan-comment-main">
                <span className="mt-plan-comment-quote">{c.quote}</span>
                <span className="mt-plan-comment-body" title={c.body}>
                  {c.body}
                </span>
              </span>
              {c.sentAt ? (
                <span className="muted mt-plan-comment-sent">{tr("plan.sent")}</span>
              ) : (
                <button
                  type="button"
                  className="ghost mt-plan-comment-del"
                  title={tr("plan.delete_comment")}
                  onClick={() => commentKey && removePlanComment(commentKey, c.id)}
                >
                  <Icon name="close" />
                </button>
              )}
            </li>
          ))}
        </ol>
      )}
      <div className="mt-plan-actions">
        {onOpen && (
          <button type="button" className="ghost mt-plan-open" onClick={onOpen}>
            <Icon name="split-horizontal" /> {tr("mirror.open_in_pane_short")}
          </button>
        )}
        {inline && (
          <button
            type="button"
            className="ghost mt-plan-open"
            aria-expanded={expanded}
            onClick={() => setExpanded((v) => !v)}
          >
            <Icon name={expanded ? "chevron-down" : "chevron-right"} />{" "}
            {expanded ? tr("mirror.plan_collapse") : tr("mirror.plan_expand")}
          </button>
        )}
        {unsent.length > 0 && onSendComments && (
          // 承認待ちなら「却下して送る」（ダイアログを閉じないと本文が届かないため
          // 却下と一体）、それ以外は普通に送るだけ。
          <button
            type="button"
            className="btn primary mt-plan-send"
            disabled={sending || !!sendDisabled}
            title={sendDisabled || undefined}
            onClick={onSendComments}
          >
            <Icon name="comment-discussion" />{" "}
            {pending
              ? tr("plan.send_and_keep_planning", { count: unsent.length })
              : tr("plan.send_comments", { count: unsent.length })}
          </button>
        )}
        {pending && (
          <>
            <button type="button" className="btn primary mt-plan-approve" disabled={sending} onClick={onApprove}>
              <Icon name="check" /> {tr("mirror.approve_run")}
            </button>
            {onReject && (
              <button
                type="button"
                className="ghost mt-plan-reject"
                disabled={sending}
                title={tr("mirror.plan_continue_title")}
                onClick={onReject}
              >
                <Icon name="close" /> {tr("mirror.reject_continue")}
              </button>
            )}
          </>
        )}
      </div>
      {inline && expanded && (
        <div className="mt-plan-body">
          <MarkdownView source={plan} />
        </div>
      )}
    </div>
  );
}

// FileThumb renders an inline preview of an image a card lists. The bytes come from
// the caller's URL builder (the mirror's downloadURL carries the tenant as a query param,
// so a plain <img src> works — no blob dance like PastedThumb, whose endpoint needs a
// header). If the fetch fails (e.g. the path isn't under a servable root), it hides itself
// so the card falls back to the icon+name row rather than showing a broken image.
export function FileThumb({ path, fileURL }: { path: string; fileURL?: (path: string) => string }) {
  const [failed, setFailed] = useState(false);
  if (failed || !fileURL) return null;
  return (
    <span className="mt-file-thumb">
      <img src={fileURL(path)} alt={baseName(path)} loading="lazy" onError={() => setFailed(true)} />
    </span>
  );
}

// UserFileBlock shows the files an agent shared via SendUserFile as a compact panel:
// an optional caption and one clickable row per file (icon + name + path), each opening
// the file in its own split pane. Image files also get an inline thumbnail so the user
// sees the picture in the mirror without opening it; clicking still opens the full
// FileView. Paths are browse-root-relative (resolved server-side); a file outside the
// browse root still lists here but will report an error on open (and its thumb hides).
export function UserFileBlock({
  files,
  caption,
  onOpen,
  fileURL,
}: {
  files?: string[];
  caption?: string;
  onOpen: (path: string) => void;
  fileURL?: (path: string) => string;
}) {
  const list = files || [];
  if (list.length === 0) return null;
  return (
    <div className="mt-files">
      <div className="mt-files-head">
        <Icon name="files" />
        <span className="mt-files-title">{tr("mirror.shared_files")}</span>
        {list.length > 1 && <span className="mt-files-count muted">{list.length}</span>}
      </div>
      {caption && <div className="mt-files-caption">{caption}</div>}
      <div className={"mt-files-list" + (list.length > 1 ? " grid" : "")}>
        {list.map((p, i) => (
          <button key={p + i} type="button" className="mt-file-item" title={tr("mirror.open_in_pane", { path: p })} onClick={() => onOpen(p)}>
            {imageFormat(p) && <FileThumb path={p} fileURL={fileURL} />}
            <span className="mt-file-top">
              <FileIcon name={baseName(p)} />
              <span className="mt-file-name">{baseName(p)}</span>
              <Icon name="split-horizontal" className="mt-file-open" />
            </span>
            <span className="mt-file-path muted">{p}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

// CopyButton copies the turn's RAW Markdown (not the rendered HTML) to the clipboard.
export function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  // ✓表示を戻すタイマー。アンマウント後の setDone を避けるため cleanup で clear する。
  const timerRef = useRef(0);
  useEffect(() => () => window.clearTimeout(timerRef.current), []);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      window.clearTimeout(timerRef.current);
      timerRef.current = window.setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context / permission) — no-op */
    }
  };
  return (
    <button
      type="button"
      className="ghost mt-copy"
      title={tr("chat.copy_md_title")}
      onClick={copy}
    >
      <Icon name={done ? "check" : "copy"} /> {done ? tr("chat.copied") : tr("chat.copy")}
    </button>
  );
}
