// SendMemoModal (docs/log/21 UI overhaul) — the selection-send step of the memo queue. Opens with
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
import { errText, type ApiError } from "../../core/api/client.ts";
import { memoFlush } from "./api.ts";
import { composeMemoMessage, splitForSend } from "./compose.ts";
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

// appendImagePaths adds the machine-facing "open with Read tool" line + the attachments'
// absolute in-container paths (mirrors buildImagePrompt / the server's buildFlushMessage)
// so whichever target the memos flush to actually opens the images. The names shown in
// the editable text stay human-readable; the paths ride only on send.
function appendImagePaths(text: string, memos: Memo[]): string {
  const paths = memos.flatMap((m) => (m.attachments || []).map((a) => a.path));
  if (!paths.length) return text;
  return text + "\n\n" + FILE_PROMPT + " " + paths.join(" ");
}

const KEEP_KEY = "af.memo-send-keep";

/** The last "Keep the memos after sending" choice of this browser; off when none is stored. */
function loadKeep(): boolean {
  try {
    return localStorage.getItem(KEEP_KEY) === "true";
  } catch {
    return false; // private mode: the preference is auxiliary
  }
}

function saveKeep(v: boolean) {
  try {
    localStorage.setItem(KEEP_KEY, String(v));
  } catch {
    // storage unavailable or full: the send itself must not be affected
  }
}

type Target = { type: "session"; name: string } | { type: "new" } | { type: "assistant"; id: string };

/** One flush of a send: the whole selection, or in a mixed one the own memos ("own") and each
 *  taken-in memo (keyed by its id). */
type Batch = { key: string; ids: string[]; text: string };

/** The batches of a send that went part way, and the session they went to. Fixed once a batch
 *  went or may have gone: a retry sends the rest of the same plan to the same session, whatever
 *  the list or the selection looks like by then. */
type Plan = { target: string; batches: Batch[] };

type BatchOutcome = { sent: number } | { error?: ApiError; unknown: boolean };

/** Whether a failed flush may still have reached the session. The CP could not tell
 *  (flush_failed); the Agent typed the text but its Enter failed (tmux_failed) or it could not
 *  confirm the delivery (502/504, passed through with the Agent's own code); a proxy answered a
 *  5xx without the CP's body; or the request itself was lost. Anything else is a refusal that
 *  sent nothing: the CP answers 200 once the send went, even when recording it failed. */
function flushOutcomeUnknown(err: ApiError | undefined): boolean {
  const code = err?.code ?? "";
  return (
    code === "flush_failed" || code === "tmux_failed" || err?.status === 502 || err?.status === 504 || /^http_5\d\d$/.test(code)
  );
}

async function flushBatch(session: string, b: Batch, keep: boolean): Promise<BatchOutcome> {
  try {
    const r = await memoFlush(session, b.ids, b.text, keep);
    if (r.error) return { error: r.error, unknown: flushOutcomeUnknown(r.error) };
    // The CP's answer to a send that went always carries the count and the ids. Anything else
    // (an empty 2xx, a proxy's JSON, a login page) says nothing about the send.
    if (Number.isInteger(r.sent) && (r.sent as number) > 0 && Array.isArray(r.ids)) return { sent: r.sent as number };
    return { unknown: true };
  } catch {
    return { unknown: true };
  }
}

interface SendMemoModalProps {
  memos: Memo[]; // the selected memos to send
  onClose: () => void;
  onSent: () => void; // bump the store after a session flush stamps them sent
  /** A send that stopped part way: some batches went, the dialog stays open for the rest. */
  onProgress?: () => void;
}

export function SendMemoModal({ memos, onClose, onSent, onProgress }: SendMemoModalProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const openStart = useSessionsStore((s) => s.openStart);
  const setSeed = useLaunchSeed((s) => s.set);
  const toast = useToast();
  const tr = useT();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  // Memos taken in from the inbox are sent on their own (ADR 0111 decision 3). With other
  // memos in the selection only a running session can take them, one message each; the
  // editable text is the member's own memos.
  const { own, origins } = useMemo(() => splitForSend(memos), [memos]);
  const mixed = origins.length > 0 && memos.length > 1;
  const [text, setText] = useState(() => composeMemoMessage(mixed ? own : memos));
  const [target, setTarget] = useState<Target | null>(null);
  const [busy, setBusy] = useState(false);
  const [keepChoice, setKeepChoice] = useState(loadKeep);
  // A send of a mixed selection that stopped part way: the plan it ran, the batches of it that
  // went (with how many memos each sent) and the ones whose outcome is unknown. A retry skips
  // what went: sending it again would deliver its text twice, and with keep nothing on the
  // server records that it went. An unknown batch stays flagged until a send of it succeeds.
  const [plan, setPlan] = useState<Plan | null>(null);
  const [done, setDone] = useState<ReadonlyMap<string, number>>(() => new Map());
  const [unknown, setUnknown] = useState<ReadonlySet<string>>(() => new Set());
  const doneIds = useMemo(
    () => new Set((plan?.batches ?? []).filter((b) => done.has(b.key)).flatMap((b) => b.ids)),
    [plan, done],
  );
  const pending = memos.filter((m) => !doneIds.has(m.id));
  // A memo that was already sent keeps its old sent_at when kept and would be swept on that
  // date, so keep is offered for unsent selections only (the server refuses it too). A batch
  // that already went is not sent again, so its stamp does not count.
  const hasSent = pending.some((m) => !!m.sentAt);
  const keep = keepChoice && !hasSent;

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

  // Waiting for input: an alive chat-capable session sitting idle — the best flush target.
  const isWaiting = (s: Session) => !!s.alive && agentOf(s.kind).caps.chat && (!s.state || s.state === "idle");
  const aliveSessions = useMemo(
    () => sessions.filter((s) => s.alive).slice().sort((a, b) => (isWaiting(b) ? 1 : 0) - (isWaiting(a) ? 1 : 0)),
    [sessions],
  );

  // Default target: the top-ranked alive session, else launching a new one.
  useEffect(() => {
    if (target) return;
    if (aliveSessions[0]) setTarget({ type: "session", name: aliveSessions[0].name });
    else if (!mixed) setTarget({ type: "new" });
  }, [aliveSessions, target, mixed]);

  const isNew = target?.type === "new";
  const canSend =
    !busy && (!!plan || (!!target && (text.trim().length > 0 || (mixed && own.length === 0)) && (!mixed || target.type === "session")));

  const targetLabel = (name: string) => {
    const s = sessions.find((x) => x.name === name);
    return s ? displayName(s) : name;
  };

  const send = async () => {
    if (!target || busy) return;
    setBusy(true);
    // Attach the images' absolute paths (kept out of the editable text) so the target
    // agent opens them; a no-op when the selection has no image attachments.
    const outText = appendImagePaths(text, memos);
    try {
      if (plan || target.type === "session") {
        // Only a send to a running session consumes memos, so only there is the choice made.
        saveKeep(keepChoice);
        // A mixed selection: the member's own memos as edited, then each taken-in memo as a
        // message of its own; the server composes those and heads them with their origin.
        const run: Plan = plan ?? {
          target: (target as { name: string }).name,
          batches: !mixed
            ? [{ key: "all", ids, text: outText }]
            : [
                ...(own.length > 0 ? [{ key: "own", ids: own.map((m) => m.id), text: appendImagePaths(text, own) }] : []),
                ...origins.map((o) => ({ key: o.id, ids: [o.id], text: "" })),
              ],
        };
        // A memo deleted or moved out of the selection since the plan was made is not sent:
        // a batch left with none is dropped, and the own text still goes for what is left of it.
        const listed = new Set(memos.map((m) => m.id));
        const went = new Map(done);
        const unsure = new Set(unknown);
        let dropped = 0;
        for (const b of run.batches) {
          if (went.has(b.key)) continue;
          const live = b.ids.filter((id) => listed.has(id));
          if (live.length === 0) {
            dropped += b.ids.length;
            continue;
          }
          const r = await flushBatch(run.target, { ...b, ids: live }, keep);
          if ("sent" in r) {
            went.set(b.key, r.sent);
            unsure.delete(b.key);
            continue;
          }
          if (r.unknown) unsure.add(b.key);
          setDone(went);
          setUnknown(unsure);
          if (went.size > 0 || unsure.size > 0) setPlan(run);
          toast(
            r.unknown
              ? t("memo.send_unknown", { target: targetLabel(run.target) })
              : errText(r.error) || t("common.send_failed"),
          );
          if (went.size > done.size) onProgress?.();
          return;
        }
        const sent = [...went.values()].reduce((a, n) => a + n, 0);
        const opens = sessionPanes(layout).get(run.target) || [];
        toast(
          <span className="toast-sent">
            {opens.map((o) => (
              <span key={o.id} className={"toast-ord " + ordClass(o.ordinal)}>
                {o.ordinal}
              </span>
            ))}
            {tr("memo.sent_n", { count: sent, target: targetLabel(run.target) })}
          </span>,
          { kind: "success" },
        );
        if (dropped > 0) toast(t("memo.send_dropped", { count: String(dropped) }));
        onSent();
        onClose();
      } else if (target.type === "new") {
        // Seed the launch hub's first prompt and open it — the launch itself (repo /
        // agent / worktree) happens in the hub. Memos stay queued until then.
        setSeed(outText);
        // A lone memo taken in from the inbox: the session it starts carries its provenance.
        if (origins.length === 1 && memos.length === 1) useLaunchSeed.getState().setInboxMemo(origins[0].id);
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
            readOnly={!!plan}
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
                className={"memo-tgt" + (on({ type: "session", name: s.name }) ? " memo-tgt--on" : "")}
                disabled={!!plan && s.name !== plan.target}
                onClick={() => setTarget({ type: "session", name: s.name })}
              >
                <span className="memo-tgt-av">
                  <Icon name={agentOf(s.kind).icon || "comment"} />
                </span>
                <span className="memo-tgt-main">
                  <span className="memo-tgt-name">{displayName(s)}</span>
                  <span className="memo-tgt-sub">{s.name}</span>
                </span>
                <span className={"memo-tgt-state" + (isWaiting(s) ? " memo-tgt-state--ok" : "")}>
                  {isWaiting(s) ? tr("memo.state_waiting") : stateInfo(s).text}
                </span>
              </button>
            ))}

            {!mixed && <div className="memo-tgroup">{tr("memo.target_other")}</div>}
            {!mixed && <button
              type="button"
              className={"memo-tgt" + (on({ type: "new" }) ? " memo-tgt--on" : "")}
              disabled={!!plan}
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
            </button>}
            {!mixed && assistants.map((a) => (
              <button
                key={a.id}
                type="button"
                className={"memo-tgt" + (on({ type: "assistant", id: a.id }) ? " memo-tgt--on" : "")}
                disabled={!!plan}
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
          {mixed && (
            <div className="ui-field-hint memo-origin-hint" role="note">
              {tr("inbox.memo_send_separately", { n: String(origins.length) })}
            </div>
          )}
          {target?.type === "session" && (
            <label className="memo-send-keep">
              <input type="checkbox" checked={keep} disabled={busy || hasSent} onChange={(e) => setKeepChoice(e.target.checked)} />
              {tr("memo.send_keep")}
            </label>
          )}
          {target?.type === "session" && hasSent && <div className="ui-field-hint">{tr("memo.send_keep_sent")}</div>}
          {plan && done.size > 0 && (
            <div className="ui-field-hint" role="note">
              {tr("memo.send_partial", {
                count: String(doneIds.size),
                total: String(plan.batches.reduce((a, b) => a + b.ids.length, 0)),
                target: targetLabel(plan.target),
              })}
            </div>
          )}
          {plan && unknown.size > 0 && (
            <div className="ui-field-hint" role="alert">
              {tr("memo.send_unknown_hint", { target: targetLabel(plan.target) })}
            </div>
          )}
          {target?.type === "session" && keep && memos.some((m) => m.state) && (
            <div className="ui-field-hint">{tr("memo.send_keep_queue")}</div>
          )}
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
