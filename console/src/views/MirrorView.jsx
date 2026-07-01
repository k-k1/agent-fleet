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
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const cursorRef = useRef(0);
  const statusRef = useRef("");
  const tickRef = useRef(null); // lets send() trigger an immediate refresh
  const bodyRef = useRef(null);
  const inputRef = useRef(null);

  // Reset accumulated turns when the session changes (cursor is a line index into
  // that session's jsonl, meaningless across sessions).
  useEffect(() => {
    cursorRef.current = 0;
    statusRef.current = "";
    setTurns([]);
    setStatus("");
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

  // Keep the conversation pinned to the latest turn.
  useEffect(() => {
    const el = bodyRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [turns]);

  // Focus the composer when this pane becomes the active chat.
  useEffect(() => {
    if (active) inputRef.current?.focus();
  }, [active]);

  const send = async () => {
    const text = draft.trim();
    if (!text || sending) return;
    setSending(true);
    setDraft("");
    statusRef.current = "working";
    setStatus("working");
    try {
      await apiJSON(`api/sessions/${q(session)}/input`, "POST", { prompt: text });
    } catch {
      /* the Agent also marks working; the next poll reconciles real state */
    }
    setSending(false);
    inputRef.current?.focus();
    // Pick up the just-logged user turn quickly rather than waiting a full interval.
    setTimeout(() => tickRef.current?.(), 250);
  };

  const onKeyDown = (e) => {
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

      <div className="mirror-body" ref={bodyRef}>
        {groups.length === 0 ? (
          <div className="mirror-empty muted">
            まだ会話はありません。下の欄からプロンプトを送るか、ターミナルで対話すると、ここに
            ターンごとの Markdown で表示されます。
          </div>
        ) : (
          groups.map((g) => <Turn key={g.idx} turn={g} />)
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
          onChange={(e) => setDraft(e.target.value)}
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
// ordered parts, and their text for copy) and drops noise. The block keeps the FIRST
// turn's idx (stable key) and timestamp (when the exchange began).
function groupTurns(turns) {
  const out = [];
  for (const t of turns) {
    if (isNoise(t)) continue;
    const parts = partsOf(t);
    if (!parts.length) continue;
    const last = out[out.length - 1];
    if (last && last.role === t.role) {
      last.parts.push(...parts);
      if (t.text) last.text += (last.text ? "\n\n" : "") + t.text;
      if (!last.model && t.model) last.model = t.model;
    } else {
      out.push({ role: t.role, parts: [...parts], text: t.text || "", model: t.model || "", ts: t.ts, idx: t.idx });
    }
  }
  return out;
}

// Turn renders one conversation block: a header (who + when + copy) and the body —
// the user's prompt as preformatted text, the assistant's reply as rendered Markdown.
function Turn({ turn }) {
  const isUser = turn.role === "user";
  return (
    <div className={"mirror-turn " + (isUser ? "user" : "assistant")}>
      <div className="mirror-turn-head">
        <span className="mt-who">{isUser ? "あなた" : "Claude"}</span>
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
            ) : (
              <MarkdownView key={i} source={p.text} />
            ),
          )
        )}
      </div>
      <div className="mirror-turn-foot">
        {turn.ts && <span className="mt-time muted">{formatTS(turn.ts)}</span>}
        <CopyButton text={turn.text} />
      </div>
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

// formatTS renders an RFC3339 timestamp as local "MM/DD HH:MM" (date kept so a long
// session that spans days stays unambiguous).
function formatTS(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
