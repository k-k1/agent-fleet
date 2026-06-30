import { useEffect, useRef, useState } from "react";
import { api, apiJSON } from "../api.js";
import { useSettings } from "../lib/settings.js";
import Icon from "../components/Icon.jsx";
import MarkdownView from "./MarkdownView.jsx";
import MirrorToggle from "../components/MirrorToggle.jsx";
import { kindIcon, kindLabel, kindShort, kindClass } from "../lib/sessionkind.js";
import { displayName, stateInfo } from "../lib/sessionview.js";

const q = encodeURIComponent;

// MirrorView is a read-mostly Markdown view of a claude session, built on the SAME
// Agent endpoints the MCP drive tools use: GET /sessions/{name}/output?since=<cursor>
// (the jsonl transcript's assistant text + a line cursor + live status) and POST
// /sessions/{name}/input (tmux send-keys). It overlays the still-mounted terminal
// (Pane keeps the PTY socket alive), so the user toggles ターミナル⇄ミラー freely.
//
// Limits (by design, see the case-A plan): the transcript is written per turn, so
// this updates per response, not token-by-token; and /output carries only assistant
// text, so a user's own prompts appear only when sent from here (optimistic echo) —
// prompts typed in the raw terminal won't show as user turns.
export default function MirrorView({ session, sessionMeta, active, mirror, onToggleMirror }) {
  const settings = useSettings();
  // "mod-enter" (default): Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe).
  // "enter": Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const [turns, setTurns] = useState([]); // {role:'user'|'assistant', text}
  const [status, setStatus] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const cursorRef = useRef(0);
  const statusRef = useRef("");
  const bodyRef = useRef(null);
  const inputRef = useRef(null);

  // Reset accumulated transcript when the session changes (cursor is a line index
  // into that session's jsonl, meaningless across sessions).
  useEffect(() => {
    cursorRef.current = 0;
    statusRef.current = "";
    setTurns([]);
    setStatus("");
  }, [session]);

  // Poll the assistant transcript since our cursor while this mirror is mounted
  // (Pane only mounts it while visible). Faster while claude is working, slower at
  // rest. A non-empty output slice is appended as one assistant turn.
  useEffect(() => {
    if (!session) return;
    let alive = true;
    let timer = null;
    const tick = async () => {
      try {
        const d = await api(`api/sessions/${q(session)}/output?since=${cursorRef.current}`);
        if (!alive) return;
        if (d && !d.error) {
          if (typeof d.cursor === "number") cursorRef.current = d.cursor;
          if (d.output && d.output.trim()) {
            setTurns((t) => [...t, { role: "assistant", text: d.output }]);
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
    tick();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [session]);

  // Keep the conversation pinned to the latest turn.
  useEffect(() => {
    const el = bodyRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [turns]);

  // Focus the composer when this pane becomes the active mirror.
  useEffect(() => {
    if (active) inputRef.current?.focus();
  }, [active]);

  const send = async () => {
    const text = draft.trim();
    if (!text || sending) return;
    setSending(true);
    setTurns((t) => [...t, { role: "user", text }]); // optimistic: /output won't echo it
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
        {turns.length === 0 ? (
          <div className="mirror-empty muted">
            まだ応答はありません。下の欄からプロンプトを送るか、ターミナルで対話すると、ここに
            Markdown で映ります。
          </div>
        ) : (
          turns.map((t, i) =>
            t.role === "user" ? (
              <div className="mirror-turn user" key={i}>
                <pre className="mirror-user-text">{t.text}</pre>
              </div>
            ) : (
              <div className="mirror-turn assistant" key={i}>
                <MarkdownView source={t.text} />
              </div>
            ),
          )
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
