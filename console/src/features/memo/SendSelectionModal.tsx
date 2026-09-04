// SendSelectionModal — sends a file, with a comment, to a running session (the coding
// agent that acts on it) or an assistant chat. Two modes:
//   - QUOTE mode (quote/startLine/endLine given): a selected range is quoted inline (a
//     Files/CodeView selection → session/assistant, docs/log/19 Phase C).
//   - FILE mode (no quote): the WHOLE file is handed by PATH reference to a SESSION — for
//     work that produces a file (e.g. "translate this manual and save it"), which the
//     chat assistant can't do (it's chat-only, no file writes). Assistants are hidden here
//     since attaching a file to a chat is the Files "open with the assistant" flow.
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { apiJSON, errText } from "../../core/api/client.ts";
import { chatCreate, assistantList } from "../chat/api.ts";
import { assistantName } from "../chat/assistantI18n.ts";
import { openChat } from "../chat/open.ts";
import { memoCreate, memoList } from "./api.ts";
import { useMemoStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { sessionPanes, ordClass } from "../../layout/badges.ts";
import { agentOf } from "../../agents/registry.ts";
import { baseName, langFor } from "../../lib/filemeta.ts";
import type { Session } from "../../types/session.ts";
import type { Assistant } from "../../types/assistant.ts";

// Remember the session the user last sent to, so it's re-selected next time (if it is
// still waiting for input). A single key — "the session I'm feeding excerpts/files to".
const LAST_SESSION_KEY = "af.sendsel.lastSession";

interface SendSelectionModalProps {
  filePath: string;
  quote?: string;
  startLine?: number;
  endLine?: number;
  onClose: () => void;
}

export function SendSelectionModal({ filePath, quote, startLine, endLine, onClose }: SendSelectionModalProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const bumpMemos = useMemoStore((s) => s.bump);
  const toast = useToast();
  const tr = useT();
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [comment, setComment] = useState("");
  const [target, setTarget] = useState(""); // "session:<name>" | "assistant:<id>"
  const [busy, setBusy] = useState(false);
  const [category, setCategory] = useState("");
  const [catSuggest, setCatSuggest] = useState<string[]>([]);

  const fileMode = quote == null; // whole file (session-only) vs an inline quote

  useEffect(() => {
    if (fileMode) return; // no assistant targets in file mode
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, [fileMode]);

  // Existing categories, to suggest while queuing (docs/log/21). Cheap membership-scoped read.
  useEffect(() => {
    memoList()
      .then((list) => setCatSuggest([...new Set((Array.isArray(list) ? list : []).map((m) => m.category).filter(Boolean))]))
      .catch(() => {});
  }, []);

  // The repo the file lives in (repos/<repo>/…), used to prefer a session in it.
  const fileRepo = filePath.startsWith("repos/") ? filePath.split("/")[1] : null;
  // "Waiting for input": an alive coding-agent session that's idle (not working/question/…) — the
  // best target since it picks the instruction up immediately (see sessionview stateInfo).
  const isWaiting = (s: Session) =>
    !!s.alive && agentOf(s.kind).caps.chat && (!s.state || s.state === "idle");
  // Rank alive sessions: same repo first, then waiting for input. Used for both the default
  // selection and the option order.
  const rank = (s: Session) => (fileRepo && s.repo === fileRepo ? 2 : 0) + (isWaiting(s) ? 1 : 0);
  const sortedSessions = useMemo(
    () => sessions.slice().sort((a, b) => rank(b) - rank(a)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sessions, fileRepo],
  );

  // Default target: the last session sent to if it's still waiting for input; else the
  // highest-ranked alive session (same repo and waiting for input preferred); else the
  // first session, else (quote mode) an assistant.
  useEffect(() => {
    if (target) return;
    const last = localStorage.getItem(LAST_SESSION_KEY);
    const lastS = last ? sessions.find((s) => s.name === last) : undefined;
    if (lastS && isWaiting(lastS)) {
      setTarget(`session:${lastS.name}`);
      return;
    }
    const best = sortedSessions.find((s) => s.alive) || sortedSessions[0];
    if (best) setTarget(`session:${best.name}`);
    else if (!fileMode && assistants[0]) setTarget(`assistant:${assistants[0].id}`);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sortedSessions, sessions, assistants, target, fileMode]);

  // The currently-selected session (if the target is a session), for the stopped guard.
  const selectedSession =
    target.startsWith("session:") ? sessions.find((s) => s.name === target.slice("session:".length)) : undefined;
  const sessionStopped = !!selectedSession && !selectedSession.alive;

  // File-mode references the file by an absolute-ish path (browse root is home) so a
  // session resolves it regardless of its own working directory.
  const pathRef = "~/" + filePath;
  const loc = fileMode ? baseName(filePath) : startLine === endLine ? `L${startLine}` : `L${startLine}–L${endLine}`;
  const composed = useMemo(() => {
    if (fileMode) {
      const c = comment.trim();
      return (c ? c + "\n\n" : "") + t("send.target_file", { path: pathRef });
    }
    const fence = "```";
    const body = `${t("send.quote_file_loc", { file: filePath, loc })}\n\n${fence}${langFor(filePath)}\n${quote}\n${fence}`;
    return comment.trim() ? `${body}\n\n${comment.trim()}` : body;
  }, [fileMode, pathRef, filePath, loc, quote, comment]);

  const isAssistant = target.startsWith("assistant:");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!target || busy || sessionStopped) return; // can't send to a stopped session
    setBusy(true);
    try {
      if (isAssistant) {
        const id = target.slice("assistant:".length);
        // Open a chat with the file attached (context) and the quote prefilled — the user
        // reviews and sends (consistent with the other assistant-open flows).
        const c = await chatCreate(id, t("send.quote_title", { name: baseName(filePath) }), { attachPath: filePath });
        if (c && c.id) {
          openChat(c.id, composed);
          onClose();
        } else toast(t("send.chat_create_failed"));
      } else {
        const name = target.slice("session:".length);
        // apiJSON resolves (doesn't throw) on a non-2xx error body, so check res.error.
        const res = await apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", { prompt: composed });
        if (res && res.error) {
          toast(errText(res.error) || t("send.send_failed_to", { name }));
          return;
        }
        localStorage.setItem(LAST_SESSION_KEY, name); // remember for next time
        // Toast with the session's display name, plus color-matched pane-number badges if
        // it's open in the main-area panes (same ordinals as the Sessions list).
        const sent = sessions.find((x) => x.name === name);
        const opens = sessionPanes(layout).get(name) || [];
        toast(
          <span className="toast-sent">
            {opens.map((o) => (
              <span key={o.id} className={"toast-ord " + ordClass(o.ordinal)}>
                {o.ordinal}
              </span>
            ))}
            {tr("send.sent_to", { name: sent ? displayName(sent) : name })}
          </span>,
          { kind: "success" },
        );
        onClose();
      }
    } catch {
      toast(t("common.send_failed"));
    } finally {
      setBusy(false);
    }
  };

  // Queue the item instead of sending now (docs/log/21): file mode → a file memo (path +
  // comment), quote mode → a text memo carrying the composed quote. repo comes from the
  // path; category is free-form (with existing-category suggestions). Doesn't need a
  // running session — the queue is flushed later from the memo queue panel.
  const addToQueue = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const input = fileMode
        ? { kind: "file" as const, repo: fileRepo || "", category: category.trim(), refPath: pathRef, body: comment.trim() }
        : { kind: "text" as const, repo: fileRepo || "", category: category.trim(), body: composed };
      const res = await memoCreate(input);
      if ((res as { error?: unknown }).error) {
        toast(t("memo.add_failed"));
        return;
      }
      bumpMemos();
      toast(t("memo.added"), { kind: "success" });
      onClose();
    } catch {
      toast(t("memo.add_failed"));
    } finally {
      setBusy(false);
    }
  };

  const noTarget = sortedSessions.length === 0 && (fileMode || assistants.length === 0);

  return (
    <Modal
      title={fileMode ? tr("send.title_file") : tr("send.title_selection")}
      onClose={onClose}
      className="send-modal"
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field">
          <div className="ui-field-label">{tr("send.destination")}</div>
          {noTarget ? (
            <div className="ui-field-hint warn">
              {tr("send.no_running")}
            </div>
          ) : (
            <select
              value={target}
              onChange={(e) => {
                setTarget(e.target.value);
                if (e.target.value.startsWith("session:"))
                  localStorage.setItem(LAST_SESSION_KEY, e.target.value.slice("session:".length));
              }}
            >
              {sortedSessions.length > 0 && (
                <optgroup label={tr("send.optgroup_session")}>
                  {sortedSessions.map((s) => (
                    <option key={s.name} value={`session:${s.name}`}>
                      {displayName(s)}（{s.name}{tr("sx.sep")}{stateInfo(s).text}
                      {fileRepo && s.repo === fileRepo ? tr("send.same_repo") : ""}）
                    </option>
                  ))}
                </optgroup>
              )}
              {!fileMode && assistants.length > 0 && (
                <optgroup label={tr("send.optgroup_assistant")}>
                  {assistants.map((a) => (
                    <option key={a.id} value={`assistant:${a.id}`}>
                      {assistantName(a)}
                    </option>
                  ))}
                </optgroup>
              )}
            </select>
          )}
          <div className={"ui-field-hint" + (sessionStopped ? " warn" : "")}>
            {sessionStopped
              ? tr("send.hint_stopped")
              : fileMode
                ? tr("send.hint_file")
                : isAssistant
                  ? tr("send.hint_assistant")
                  : tr("send.hint_session")}
          </div>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("send.comment_label")}</div>
          <textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            onKeyDown={(e) => {
              // Ctrl/⌘+Enter submits (Enter alone keeps making newlines in the comment).
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                e.preventDefault();
                e.currentTarget.form?.requestSubmit();
              }
            }}
            rows={3}
            placeholder={
              fileMode
                ? tr("send.ph_file")
                : tr("send.ph_selection")
            }
            autoFocus
          />
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("send.category_label")}</div>
          <input
            type="text"
            list="sendsel-cat-suggest"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            placeholder={tr("send.category_ph")}
          />
          <datalist id="sendsel-cat-suggest">
            {catSuggest.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("send.preview_label", { loc })}</div>
          <pre className="send-preview">{composed}</pre>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        <Button variant="ghost" disabled={busy} onClick={() => void addToQueue()}>
          {tr("send.add_to_queue")}
        </Button>
        <Button variant="primary" type="submit" disabled={!target || busy || sessionStopped}>
          {isAssistant ? tr("send.open_assistant") : tr("send.send_to_session")}
        </Button>
      </footer>
    </Modal>
  );
}
