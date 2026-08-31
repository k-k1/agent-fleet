// SendMemoModal (docs/log/21 UI刷新) — the selection-send step of the memo queue. Opens with
// the selected memos concatenated into one editable message, then sends to a chosen
// destination:
//   - a running session (memoFlush with the edited text — sends once + stamps them sent),
//   - a NEW session (seeds the launch hub's first prompt with the text and opens it — the
//     memos stay queued until the user actually launches), or
//   - an assistant chat (opens a chat prefilled with the text — memos stay queued).
// The composed default mirrors the server's buildFlushMessage so "send as-is" is identical
// to the old one-tap flush.
import { useMemo, useState, useEffect, useRef } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { memoFlush } from "./api.ts";
import { FILE_PROMPT } from "../../lib/pastedImages.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useLaunchSeed } from "../repos/store.ts";
import { chatCreate, assistantList } from "../chat/api.ts";
import { assistantName } from "../chat/assistantI18n.ts";
import { openChat } from "../chat/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { sessionPanes, ordClass } from "../../layout/badges.ts";
import type { Memo } from "../../types/memo.ts";
import type { Session } from "../../types/session.ts";
import type { Assistant } from "../../types/assistant.ts";

// Concatenate the selected memos directly, grouped by category, mirroring the server's
// buildFlushMessage (memo.go) so an unedited send is byte-for-byte the server flush.
export function composeMemoMessage(memos: Memo[]): string {
  const sorted = memos.slice().sort((a, b) => (a.category < b.category ? -1 : a.category > b.category ? 1 : 0));
  const lines: string[] = [];
  let lastCat = "\x00";
  let n = 0;
  for (const m of sorted) {
    if (m.category !== lastCat) {
      lastCat = m.category;
      n = 0;
      if (lines.length) lines.push("");
      lines.push("## " + (m.category || t("memo.uncategorized")));
    }
    n++;
    if (m.kind === "file") {
      lines.push(`${n}. ${t("memo.flush_file", { path: m.refPath })}`);
      if (m.body) lines.push("   " + m.body);
    } else if (m.body) {
      lines.push(`${n}. ${m.body}`);
    } else {
      lines.push(`${n}. ${t("memo.flush_image_only")}`);
    }
    if (m.attachments?.length) {
      lines.push("   " + t("memo.flush_images", { names: m.attachments.map((a) => a.name).join(", ") }));
    }
  }
  return lines.join("\n");
}

// appendImagePaths adds the machine-facing "open with Read tool" line + the attachments'
// absolute in-container paths (mirrors buildImagePrompt / the server's buildFlushMessage)
// so whichever target the memos flush to actually opens the images. The names shown in
// the editable text stay human-readable; the paths ride only on send.
function appendImagePaths(text: string, memos: Memo[]): string {
  const paths = memos.flatMap((m) => (m.attachments || []).map((a) => a.path));
  if (!paths.length) return text;
  return text + "\n\n" + FILE_PROMPT + " " + paths.join(" ");
}

type Target = { type: "session"; name: string } | { type: "new" } | { type: "assistant"; id: string };

interface SendMemoModalProps {
  memos: Memo[]; // the selected memos to send
  onClose: () => void;
  onSent: () => void; // bump the store after a session flush stamps them sent
}

export function SendMemoModal({ memos, onClose, onSent }: SendMemoModalProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const openStart = useSessionsStore((s) => s.openStart);
  const setSeed = useLaunchSeed((s) => s.set);
  const toast = useToast();
  const tr = useT();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [text, setText] = useState(() => composeMemoMessage(memos));
  const [target, setTarget] = useState<Target | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (textareaRef.current) {
      textareaRef.current.focus();
    }
  }, []);

  const ids = useMemo(() => memos.map((m) => m.id), [memos]);

  // 入力待ち: an alive chat-capable session sitting idle — the best flush target.
  const isWaiting = (s: Session) => !!s.alive && agentOf(s.kind).caps.chat && (!s.state || s.state === "idle");
  const aliveSessions = useMemo(
    () => sessions.filter((s) => s.alive).slice().sort((a, b) => (isWaiting(b) ? 1 : 0) - (isWaiting(a) ? 1 : 0)),
    [sessions],
  );

  // Default target: the top-ranked alive session, else 新規起動.
  useEffect(() => {
    if (target) return;
    setTarget(aliveSessions[0] ? { type: "session", name: aliveSessions[0].name } : { type: "new" });
  }, [aliveSessions, target]);

  const isNew = target?.type === "new";
  const canSend = !!target && !busy && text.trim().length > 0;

  const send = async () => {
    if (!target || busy) return;
    setBusy(true);
    // Attach the images' absolute paths (kept out of the editable text) so the target
    // agent opens them; a no-op when the selection has no image attachments.
    const outText = appendImagePaths(text, memos);
    try {
      if (target.type === "session") {
        const res = await memoFlush(target.name, ids, outText);
        if (res.error) {
          toast(errText(res.error) || t("common.send_failed"));
          return;
        }
        const s = sessions.find((x) => x.name === target.name);
        const opens = sessionPanes(layout).get(target.name) || [];
        toast(
          <span className="toast-sent">
            {opens.map((o) => (
              <span key={o.id} className={"toast-ord " + ordClass(o.ordinal)}>
                {o.ordinal}
              </span>
            ))}
            {tr("memo.sent_n", { count: res.sent ?? ids.length, target: s ? displayName(s) : target.name })}
          </span>,
          { kind: "success" },
        );
        onSent();
        onClose();
      } else if (target.type === "new") {
        // Seed the launch hub's first prompt and open it — the launch itself (repo /
        // agent / worktree) happens in the hub. Memos stay queued until then.
        setSeed(outText);
        openStart();
        toast(t("memo.new_session_started"), { kind: "success" });
        onClose();
      } else {
        const c = await chatCreate(target.id, t("memo.assistant_title"));
        if (c && c.id) {
          openChat(c.id, outText);
          onClose();
        } else {
          toast(t("send.chat_create_failed"));
        }
      }
    } catch {
      toast(t("common.send_failed"));
    } finally {
      setBusy(false);
    }
  };

  const on = (tg: Target) =>
    target?.type === tg.type &&
    (tg.type !== "session" || (target as { name: string }).name === tg.name) &&
    (tg.type !== "assistant" || (target as { id: string }).id === tg.id);

  return (
    <Modal title={tr("memo.send_title")} onClose={onClose} className="memo-send-modal" lockClose={busy}>
      <div className="ui-modal-body">
        <div className="ui-field">
          <div className="ui-field-label">
            {tr("memo.content_label")} <span className="memo-send-hint">{tr("memo.content_hint")}</span>
          </div>
          <textarea
            ref={textareaRef}
            className="memo-send-text"
            value={text}
            onChange={(e) => setText(e.target.value)}
            spellCheck={false}
            rows={9}
          />
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("memo.target_label")}</div>
          <div className="memo-targets">
            {aliveSessions.length > 0 && <div className="memo-tgroup">{tr("memo.target_running")}</div>}
            {aliveSessions.map((s) => (
              <button
                key={s.name}
                type="button"
                className={"memo-tgt" + (on({ type: "session", name: s.name }) ? " on" : "")}
                onClick={() => setTarget({ type: "session", name: s.name })}
              >
                <span className="memo-tgt-av">
                  <Icon name={agentOf(s.kind).icon || "comment"} />
                </span>
                <span className="memo-tgt-main">
                  <span className="memo-tgt-name">{displayName(s)}</span>
                  <span className="memo-tgt-sub">{s.name}</span>
                </span>
                <span className={"memo-tgt-state" + (isWaiting(s) ? " ok" : "")}>
                  {isWaiting(s) ? tr("memo.state_waiting") : stateInfo(s).text}
                </span>
              </button>
            ))}

            <div className="memo-tgroup">{tr("memo.target_other")}</div>
            <button
              type="button"
              className={"memo-tgt" + (on({ type: "new" }) ? " on" : "")}
              onClick={() => setTarget({ type: "new" })}
            >
              <span className="memo-tgt-av">
                <Icon name="add" />
              </span>
              <span className="memo-tgt-main">
                <span className="memo-tgt-name">{tr("memo.target_new")}</span>
                <span className="memo-tgt-sub">{tr("memo.target_new_sub")}</span>
              </span>
              <span className="memo-tgt-state new">NEW</span>
            </button>
            {assistants.map((a) => (
              <button
                key={a.id}
                type="button"
                className={"memo-tgt" + (on({ type: "assistant", id: a.id }) ? " on" : "")}
                onClick={() => setTarget({ type: "assistant", id: a.id })}
              >
                <span className="memo-tgt-av">
                  <Icon name="sparkle" />
                </span>
                <span className="memo-tgt-main">
                  <span className="memo-tgt-name">{assistantName(a)}</span>
                  <span className="memo-tgt-sub">{tr("memo.target_assistant_sub")}</span>
                </span>
              </button>
            ))}
          </div>
          <div className="ui-field-hint">
            {isNew ? tr("memo.new_hint") : target?.type === "assistant" ? tr("memo.assistant_hint") : ""}
          </div>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" disabled={!canSend} onClick={() => void send()}>
          {isNew ? tr("memo.launch_send") : tr("common.send")}
        </Button>
      </footer>
    </Modal>
  );
}
