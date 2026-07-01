import { useEffect, useRef, useState } from "react";
import { api, apiJSON } from "../api.js";
import { useSettings } from "../lib/settings.js";
import Icon from "../components/Icon.jsx";
import MarkdownView from "./MarkdownView.jsx";
import MirrorToggle from "../components/MirrorToggle.jsx";
import { kindIcon, kindLabel, kindShort, kindClass } from "../lib/sessionkind.js";
import { displayName, stateInfo } from "../lib/sessionview.js";

const q = encodeURIComponent;

// MirrorView (user-facing: チャット) is a read-mostly Markdown view of a claude
// session, built on the same Agent endpoints the MCP drive tools use: GET
// /sessions/{name}/messages?since=<cursor> (the jsonl transcript as structured turns
// — role + Markdown text + timestamp — plus a line cursor and live status) and POST
// /sessions/{name}/input (tmux send-keys). It overlays the still-mounted terminal
// (Pane keeps the PTY socket alive), so the user toggles ターミナル⇄チャット freely.
//
// Limits (case-A): the transcript is written per turn, so turns appear per response,
// not token-by-token. Prompts typed in the raw terminal DO appear (they're logged as
// user turns), just at the next poll.
export default function MirrorView({ session, sessionMeta, active, mirror, onToggleMirror }) {
  const settings = useSettings();
  // "mod-enter" (default): Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe).
  // "enter": Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [turns, setTurns] = useState([]); // {role:'user'|'assistant', text, ts, idx}
  const [status, setStatus] = useState("");
  const [pending, setPending] = useState(null); // currently-awaiting AskUserQuestion
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [histIdx, setHistIdx] = useState(null); // position in composer history, or null
  const cursorRef = useRef(0);
  const statusRef = useRef("");
  const tickRef = useRef(null); // lets send() trigger an immediate refresh
  const bodyRef = useRef(null);
  const inputRef = useRef(null);
  const historyRef = useRef([]); // prompts sent from the composer (oldest→newest)

  // Reset accumulated turns when the session changes (cursor is a line index into
  // that session's jsonl, meaningless across sessions).
  useEffect(() => {
    cursorRef.current = 0;
    statusRef.current = "";
    historyRef.current = [];
    setTurns([]);
    setStatus("");
    setPending(null);
    setHistIdx(null);
  }, [session]);

  // Poll the transcript since our cursor while this view is mounted (Pane only mounts
  // it while visible). Faster while claude is working, slower at rest. New turns are
  // appended; the cursor advances by the transcript's line count.
  useEffect(() => {
    if (!session) return;
    let alive = true;
    let timer = null;
    const tick = async () => {
      try {
        const d = await api(`api/sessions/${q(session)}/messages?since=${cursorRef.current}`);
        if (!alive) return;
        if (d && !d.error) {
          if (typeof d.cursor === "number") cursorRef.current = d.cursor;
          if (Array.isArray(d.messages) && d.messages.length) {
            setTurns((t) => [...t, ...d.messages]);
          }
          if (d.status) {
            statusRef.current = d.status;
            setStatus(d.status);
          }
          setPending(Array.isArray(d.pendingQuestions) ? d.pendingQuestions : null);
        }
      } catch {
        /* transient; retry on the next tick */
      }
      if (!alive) return;
      timer = setTimeout(tick, statusRef.current === "working" ? 1200 : 3000);
    };
    tickRef.current = () => {
      if (timer) clearTimeout(timer);
      tick();
    };
    tick();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
      tickRef.current = null;
    };
  }, [session]);

  // Keep the conversation pinned to the latest turn / pending question / typing dots.
  useEffect(() => {
    const el = bodyRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [turns, pending, status]);

  // Focus the composer when this pane becomes the active chat.
  useEffect(() => {
    if (active) inputRef.current?.focus();
  }, [active]);

  // Low-level: type one prompt into the session (tmux send-keys). No state/guard.
  const postInput = async (text) => {
    try {
      await apiJSON(`api/sessions/${q(session)}/input`, "POST", { prompt: text });
    } catch {
      /* the Agent also marks working; the next poll reconciles real state */
    }
  };

  // sendPrompt submits one prompt (the composer, or a single-select answer).
  const sendPrompt = async (text) => {
    const t = (text || "").trim();
    if (!t || sending) return;
    setSending(true);
    statusRef.current = "working";
    setStatus("working");
    await postInput(t);
    setSending(false);
    // Pick up the just-logged user turn quickly rather than waiting a full interval.
    setTimeout(() => tickRef.current?.(), 250);
  };

  // sendKeys drives the AskUserQuestion modal via named keys (Down/Space/Enter), the
  // only way to answer multi-select / multi-question forms (free text can't).
  const sendKeys = async (keys) => {
    if (!keys || !keys.length || sending) return;
    setSending(true);
    statusRef.current = "working";
    setStatus("working");
    try {
      await apiJSON(`api/sessions/${q(session)}/input`, "POST", { keys });
    } catch {
      /* next poll reconciles */
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
  };


  const send = async () => {
    const text = draft.trim();
    if (!text) return;
    const h = historyRef.current;
    if (h[h.length - 1] !== text) h.push(text); // skip consecutive duplicates
    setHistIdx(null);
    setDraft("");
    await sendPrompt(text);
    inputRef.current?.focus();
  };

  const onKeyDown = (e) => {
    // Shell-style history: ↑/↓ recall past prompts when the field is empty (or once
    // recall is underway). With text present, arrows move the caret as usual.
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.nativeEvent.isComposing) {
      const h = historyRef.current;
      const navigating = histIdx !== null;
      if (e.key === "ArrowUp" && (draft === "" || navigating) && h.length) {
        e.preventDefault();
        const ni = navigating ? Math.max(0, histIdx - 1) : h.length - 1;
        setHistIdx(ni);
        setDraft(h[ni]);
        return;
      }
      if (e.key === "ArrowDown" && navigating) {
        e.preventDefault();
        const ni = histIdx + 1;
        if (ni >= h.length) {
          setHistIdx(null);
          setDraft("");
        } else {
          setHistIdx(ni);
          setDraft(h[ni]);
        }
        return;
      }
    }
    // Don't intercept Enter while an IME candidate window is open (JP/CJK input).
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    if (modSend) {
      // Ctrl/⌘+Enter submits; plain Enter falls through to insert a newline.
      if (mod) {
        e.preventDefault();
        send();
      }
    } else if (!e.shiftKey && !mod) {
      // Enter submits; Shift+Enter falls through to insert a newline.
      e.preventDefault();
      send();
    }
  };

  // claude writes one logical response as several assistant events (text split by
  // tool calls), so merge consecutive same-role turns into one block and drop the
  // system-injected user lines (bash i/o, task notifications, slash-command echoes).
  const groups = groupTurns(turns);

  // A /context-like gauge: the newest assistant turn's prompt size (input + cache) is
  // the current context fill. The per-category split (/context) is computed inside
  // claude and isn't in the transcript, but the cache breakdown is real usage data.
  const ctxUsage = latestContext(groups);

  // Status chip: prefer the live polled status, fall back to the session meta.
  const chip = status
    ? stateInfo({ kind: "claude", alive: status !== "stopped", state: status })
    : sessionMeta
      ? stateInfo(sessionMeta)
      : null;

  return (
    <div className="mirrorview">
      <header className="view-head">
        {sessionMeta ? (
          <span className="pane-session">
            <span className={"kind-tag kind-" + kindClass(sessionMeta.kind)}>
              <Icon name={kindIcon(sessionMeta.kind)} />
              <span className="kt-label kt-full">{kindLabel(sessionMeta.kind)}</span>
              <span className="kt-label kt-short">{kindShort(sessionMeta.kind)}</span>
            </span>
            <span className="session-display">{displayName(sessionMeta)}</span>
            <span className="session-name">{sessionMeta.name}</span>
            {chip && (
              <span className={"session-state " + chip.cls}>
                <Icon name={chip.icon} spin={chip.spin} /> {chip.text}
              </span>
            )}
          </span>
        ) : (
          <span className="view-title">session: {session}</span>
        )}
        <MirrorToggle mirror={mirror} onToggle={onToggleMirror} />
      </header>

      {ctxUsage && <ContextBar {...ctxUsage} />}

      <div className="mirror-body" ref={bodyRef}>
        {groups.length === 0 && !pending ? (
          <div className="mirror-empty muted">
            まだ会話はありません。下の欄からプロンプトを送るか、ターミナルで対話すると、ここに
            ターンごとの Markdown で表示されます。
          </div>
        ) : (
          renderGroups(groups, sendPrompt)
        )}
        {pending && pending.length > 0 && (
          <div className="mirror-turn assistant">
            <div className="mirror-turn-head">
              <span className="mt-who">Claude</span>
              <span className="mt-model muted">質問中</span>
            </div>
            <div className="mirror-turn-body">
              <PendingQuestions
                key={"pq-" + (pending[0]?.question || "")}
                questions={pending}
                sending={sending}
                onSendOne={sendPrompt}
                onSubmitKeys={sendKeys}
              />
            </div>
          </div>
        )}
        {status === "working" && !pending && (
          <div className="mirror-typing" aria-label="Claude が入力中">
            <span className="mt-who">Claude</span>
            <span className="typing-dots">
              <i />
              <i />
              <i />
            </span>
          </div>
        )}
      </div>

      <div className="mirror-compose">
        <textarea
          ref={inputRef}
          className="mirror-input"
          rows={2}
          placeholder={
            modSend
              ? "プロンプトを入力（Ctrl+Enter で送信 / Enter で改行）"
              : "プロンプトを入力（Enter で送信 / Shift+Enter で改行）"
          }
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setHistIdx(null); // typing leaves history-recall mode
          }}
          onKeyDown={onKeyDown}
        />
        <button
          type="button"
          className="btn primary mirror-send"
          disabled={!draft.trim() || sending}
          onClick={send}
          title="送信"
        >
          <Icon name="send" />
        </button>
      </div>
    </div>
  );
}

// System-injected user lines that aren't real prompts: slash-command echoes, the
// bash tool's stdin/stdout, task-notification frames, memory captures. We hide them
// so the chat reads as the actual conversation. Matched at the start of the text.
const SYS_PREFIXES = [
  "<task-notification>",
  "<bash-input>",
  "<bash-stdout>",
  "<bash-stderr>",
  "<local-command",
  "<command-message>",
  "<command-name>",
  "<command-args>",
  "<user-memory-input>",
  "<system-reminder>",
];

function isNoise(t) {
  if (t.role !== "user") return false;
  const s = (t.text || "").replace(/^\s+/, "");
  return SYS_PREFIXES.some((p) => s.startsWith(p));
}

// partsOf returns a turn's ordered parts, synthesizing a single text part for turns
// from an older Agent that predates the parts field (backward compatible).
function partsOf(t) {
  if (Array.isArray(t.parts) && t.parts.length) return t.parts;
  return t.text ? [{ kind: "text", text: t.text }] : [];
}

// groupTurns folds consecutive same-role turns into one block (concatenating their
// ordered parts, and their text for copy) and drops noise. A block breaks on a role
// OR sidechain change so a subagent's turns stay separate from the main thread. It
// keeps the FIRST turn's idx/timestamp/branch/cwd, and for tokens sums output while
// taking the last event's input/cache as the context size.
function groupTurns(turns) {
  const out = [];
  for (const t of turns) {
    if (isNoise(t)) continue;
    const parts = partsOf(t);
    if (!parts.length) continue;
    const last = out[out.length - 1];
    if (last && last.role === t.role && last.sidechain === !!t.sidechain) {
      last.parts.push(...parts);
      if (t.text) last.text += (last.text ? "\n\n" : "") + t.text;
      if (!last.model && t.model) last.model = t.model;
      last.outTok += t.outTok || 0;
      if (t.inTok || t.cacheRead || t.cacheCreate) {
        last.inTok = t.inTok || 0;
        last.cacheRead = t.cacheRead || 0;
        last.cacheCreate = t.cacheCreate || 0;
      }
    } else {
      out.push({
        role: t.role,
        sidechain: !!t.sidechain,
        parts: [...parts],
        text: t.text || "",
        model: t.model || "",
        branch: t.branch || "",
        cwd: t.cwd || "",
        inTok: t.inTok || 0,
        outTok: t.outTok || 0,
        cacheRead: t.cacheRead || 0,
        cacheCreate: t.cacheCreate || 0,
        ts: t.ts,
        idx: t.idx,
      });
    }
  }
  return out;
}

// latestContext returns the newest assistant turn's prompt breakdown (reused-cache,
// newly-cached, fresh) — the current context fill — or null if no usage is recorded
// yet (e.g. an Agent that predates the usage field, before a Stop/Start).
function latestContext(groups) {
  for (let i = groups.length - 1; i >= 0; i--) {
    const g = groups[i];
    if (g.role === "user") continue;
    if (g.inTok + g.cacheRead + g.cacheCreate > 0) {
      return { read: g.cacheRead, create: g.cacheCreate, fresh: g.inTok, model: g.model };
    }
  }
  return null;
}

// contextWindow returns the model's context length. The current Claude family
// (Opus 4.6/4.7/4.8, Sonnet 4.6, Fable/Mythos 5) is 1M-native — 1M is the default
// window, not a 200k default you grow into. Haiku is 200k. Unknown/older models
// assume 200k but grow to fit if a 1M beta is clearly in use.
function contextWindow(model, used) {
  const m = (model || "").toLowerCase();
  if (/opus-4-[678]|sonnet-4-6|fable-5|mythos-5/.test(m)) return 1000000;
  if (/haiku/.test(m)) return 200000;
  return used > 200000 ? 1000000 : 200000;
}

// ContextBar is a /context-like fill gauge for the context window, segmented by how
// the prompt tokens break down (cache read / cache creation / fresh input).
function ContextBar({ read, create, fresh, model }) {
  const used = read + create + fresh;
  const window = contextWindow(model, used);
  const pct = Math.min(100, (used / window) * 100);
  const w = (n) => (n / window) * 100 + "%";
  const title =
    `文脈 ${used.toLocaleString()} / ${window.toLocaleString()} トークン (${pct.toFixed(0)}%)\n` +
    `キャッシュ再利用 ${read.toLocaleString()} · 新規キャッシュ ${create.toLocaleString()} · 未キャッシュ ${fresh.toLocaleString()}`;
  return (
    <div className="mirror-ctxbar" title={title}>
      <div className="cb-track">
        <div className="cb-seg cb-read" style={{ width: w(read) }} />
        <div className="cb-seg cb-create" style={{ width: w(create) }} />
        <div className="cb-seg cb-fresh" style={{ width: w(fresh) }} />
      </div>
      <span className="cb-label muted">
        コンテキスト {fmtTok(used)} / {fmtTok(window)}・{pct.toFixed(0)}%
      </span>
    </div>
  );
}

// renderGroups lays the blocks out, inserting a context strip (branch · cwd) above a
// block whenever either changes from the previously shown one — so a branch switch or
// cd is marked once, not repeated on every turn. Empty context leaves the marker as-is.
function renderGroups(groups, onAnswer) {
  const els = [];
  let prevCtx = "";
  for (const g of groups) {
    const ctx = g.branch || g.cwd ? (g.branch || "") + " " + (g.cwd || "") : "";
    if (ctx && ctx !== prevCtx) {
      els.push(<ContextLine key={"ctx-" + g.idx} branch={g.branch} cwd={g.cwd} />);
    }
    if (ctx) prevCtx = ctx;
    els.push(<Turn key={g.idx} turn={g} onAnswer={onAnswer} />);
  }
  return els;
}

// ContextLine marks the git branch / working dir in effect from here on.
function ContextLine({ branch, cwd }) {
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

// Turn renders one conversation block: a header (who + model), the body (user prompt
// as text, assistant reply as Markdown with faint tool traces), and a footer (time +
// token usage + copy). Subagent (sidechain) turns get a distinct label and tint.
function Turn({ turn, onAnswer }) {
  const isUser = turn.role === "user";
  const who = isUser ? "あなた" : turn.sidechain ? "サブエージェント" : "Claude";
  const ctxTok = turn.inTok + turn.cacheRead + turn.cacheCreate;
  return (
    <div className={"mirror-turn " + (isUser ? "user" : "assistant") + (turn.sidechain ? " sidechain" : "")}>
      <div className="mirror-turn-head">
        <span className="mt-who">{who}</span>
        {!isUser && turn.model && <span className="mt-model">{prettyModel(turn.model)}</span>}
      </div>
      <div className="mirror-turn-body">
        {isUser ? (
          <pre className="mirror-user-text">{turn.text}</pre>
        ) : (
          turn.parts.map((p, i) =>
            p.kind === "tool" ? (
              // A faint trace of what claude did between paragraphs (Read/Bash/…).
              <div className="mt-tool" key={i}>
                <Icon name="tools" />
                <span className="mt-tool-name">{p.tool}</span>
                {p.info && <span className="mt-tool-info">{p.info}</span>}
              </div>
            ) : p.kind === "question" ? (
              // A question from the transcript is already answered (claude writes the
              // tool_use only after the answer) — show it resolved, not clickable.
              <QuestionBlock key={i} questions={p.questions} answered answer={p.answer} />
            ) : (
              <MarkdownView key={i} source={p.text} />
            ),
          )
        )}
      </div>
      <div className="mirror-turn-foot">
        {turn.ts && <span className="mt-time muted">{formatTS(turn.ts)}</span>}
        {turn.outTok > 0 && (
          <span className="mt-tok muted" title="入力(文脈)↑ / 出力↓ トークン">
            ↑{fmtTok(ctxTok)} ↓{fmtTok(turn.outTok)}
          </span>
        )}
        <CopyButton text={turn.text} />
      </div>
    </div>
  );
}

// PendingQuestions is the interactive form for the currently-awaiting AskUserQuestion.
// One question with a single choice → click-to-send (the common, low-friction case).
// Multi-select or multiple questions → build a selection, then submit: answers are
// sent one page at a time (multi-select choices joined) so the terminal modal advances
// through each question and doesn't close after the first pick.
function PendingQuestions({ questions, onSendOne, onSubmitKeys, sending }) {
  const qs = questions || [];
  const [sel, setSel] = useState(() => qs.map(() => []));
  const single = qs.length === 1 && !qs[0]?.multiSelect;

  const toggle = (qi, label, multi) => {
    setSel((prev) => {
      const next = prev.map((a) => a.slice());
      const cur = next[qi] || [];
      if (multi) next[qi] = cur.includes(label) ? cur.filter((x) => x !== label) : [...cur, label];
      else next[qi] = cur[0] === label ? [] : [label];
      return next;
    });
  };

  // Every single-select question needs a pick before we can drive the modal.
  const canSubmit = qs.every((q, qi) => q.multiSelect || (sel[qi] || []).length > 0);

  // Drive the modal with named keys, matching the real AskUserQuestion behavior
  // (verified against the terminal). Each question page starts with the cursor at the
  // top option; ↑/↓ navigate options, ←/→ switch question tabs, Enter selects/toggles.
  //   single-select: move Down to the choice, Enter — this selects AND auto-advances
  //                  to the next tab.
  //   multi-select:  Enter TOGGLES in place (cursor stays); after toggling every
  //                  choice, Right advances to the next tab.
  // After all questions we land on the Submit tab (Review page); a final Enter
  // activates "Submit answers".
  const submit = () => {
    const keys = [];
    qs.forEach((q, qi) => {
      const opts = q.options || [];
      const idx = (sel[qi] || [])
        .map((l) => opts.findIndex((o) => o.label === l))
        .filter((i) => i >= 0)
        .sort((a, b) => a - b);
      if (q.multiSelect) {
        let cur = 0;
        for (const ci of idx) {
          for (let k = 0; k < ci - cur; k++) keys.push("Down");
          keys.push("Enter"); // toggle in place
          cur = ci;
        }
        keys.push("Right"); // advance to the next question / Submit tab
      } else {
        const ci = idx[0] ?? 0;
        for (let k = 0; k < ci; k++) keys.push("Down");
        keys.push("Enter"); // select + auto-advance to the next tab
      }
    });
    keys.push("Enter"); // Review page: "Submit answers"
    onSubmitKeys(keys);
  };

  return (
    <div className="mt-question">
      {qs.map((qn, qi) => (
        <div className="mq" key={qi}>
          <div className="mq-head">
            <Icon name="comment-discussion" />
            {qn.header && <span className="mq-header">{qn.header}</span>}
            {qs.length > 1 && (
              <span className="mq-page muted">
                {qi + 1}/{qs.length}
              </span>
            )}
            {qn.multiSelect && <span className="mq-multi muted">複数選択</span>}
          </div>
          {qn.question && <div className="mq-text">{qn.question}</div>}
          <div className="mq-options">
            {(qn.options || []).map((o, oi) => {
              const checked = (sel[qi] || []).includes(o.label);
              return (
                <button
                  type="button"
                  className={"mq-opt" + (checked ? " checked" : "")}
                  key={oi}
                  disabled={sending}
                  onClick={() => (single ? onSendOne(o.label) : toggle(qi, o.label, qn.multiSelect))}
                  title={o.description || o.label}
                >
                  {!single && (
                    <span className="mq-mark">{qn.multiSelect ? (checked ? "☑" : "☐") : checked ? "◉" : "○"}</span>
                  )}
                  <span className="mq-opt-body">
                    <span className="mq-opt-label">{o.label}</span>
                    {o.description && <span className="mq-opt-desc">{o.description}</span>}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      ))}
      {!single && (
        <div className="mq-submit-row">
          <button
            type="button"
            className="btn primary mq-submit"
            disabled={sending || !canSubmit}
            onClick={submit}
          >
            回答を送信
          </button>
        </div>
      )}
    </div>
  );
}

// QuestionBlock renders an already-answered AskUserQuestion from the transcript:
// header + prompt + options, inert, with the chosen option highlighted.
function QuestionBlock({ questions, answered, answer }) {
  const norm = (answer || "").trim();
  return (
    <div className={"mt-question" + (answered ? " answered" : "")}>
      {(questions || []).map((qn, qi) => {
        const opts = qn.options || [];
        // Which options the answer picked. The answer is a free-text reply that may
        // list several labels ("AWS, セルフホスト"), so match every label present as a
        // token; fall back to containment when no token matches exactly.
        const tokens = norm ? norm.split(/[,、\/\s]+/).map((s) => s.trim()).filter(Boolean) : [];
        let chosen = answered ? opts.filter((o) => tokens.includes(o.label)) : [];
        if (answered && !chosen.length && norm) chosen = opts.filter((o) => norm.includes(o.label));
        const chosenSet = new Set(chosen);
        return (
          <div className="mq" key={qi}>
            <div className="mq-head">
              <Icon name="comment-discussion" />
              {qn.header && <span className="mq-header">{qn.header}</span>}
              {qn.multiSelect && <span className="mq-multi muted">複数選択可</span>}
              {answered && <span className="mq-done muted">回答済み</span>}
            </div>
            {qn.question && <div className="mq-text">{qn.question}</div>}
            <div className="mq-options">
              {opts.map((o, oi) => {
                const sel = chosenSet.has(o);
                return (
                  <button
                    type="button"
                    className={"mq-opt" + (sel ? " selected" : "")}
                    key={oi}
                    disabled
                    title={o.description || o.label}
                  >
                    <span className="mq-mark">{sel ? "✔" : qn.multiSelect ? "☐" : "○"}</span>
                    <span className="mq-opt-body">
                      <span className="mq-opt-label">{o.label}</span>
                      {o.description && <span className="mq-opt-desc">{o.description}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
            {answered && norm && !chosenSet.size && <div className="mq-answer muted">回答: {norm}</div>}
          </div>
        );
      })}
    </div>
  );
}

// CopyButton copies the turn's RAW Markdown (not the rendered HTML) to the clipboard.
function CopyButton({ text }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context / permission) — no-op */
    }
  };
  return (
    <button
      type="button"
      className="ghost mt-copy"
      title="Markdown をコピー"
      onClick={copy}
    >
      <Icon name={done ? "check" : "copy"} /> {done ? "コピー済" : "コピー"}
    </button>
  );
}

// prettyModel shortens a model id for the turn header: "claude-opus-4-8" → "opus 4.8".
function prettyModel(m) {
  return m
    .replace(/^claude-/, "")
    .replace(/-(\d+)-(\d+)$/, " $1.$2")
    .replace(/-latest$/, "");
}

// prettyCwd collapses the home prefix to ~ so the working dir reads compactly.
function prettyCwd(p) {
  return p.replace(/^\/home\/[^/]+/, "~");
}

// fmtTok renders a token count compactly: 927 → "927", 30371 → "30k", 1000000 → "1M".
function fmtTok(n) {
  if (!n) return "0";
  if (n >= 1e6) return (n / 1e6).toFixed(n < 1e7 ? 1 : 0).replace(/\.0$/, "") + "M";
  if (n < 1000) return String(n);
  return (n / 1000).toFixed(n < 10000 ? 1 : 0).replace(/\.0$/, "") + "k";
}

// formatTS renders an RFC3339 timestamp as local "MM/DD HH:MM" (date kept so a long
// session that spans days stays unambiguous).
function formatTS(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
