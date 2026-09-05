// Work-plan panel (docs/log/33 stage 5).
//
// The surface for viewing, editing and re-deriving the conversation's slot that is carried
// verbatim across compaction. Where the compaction hand-over summary (the notice card) is an
// LLM summary and is rewritten every time, the text here reaches the next session unchanged,
// which makes it the place a user puts what the assistant must never forget.
//
// Of the three update triggers (listed at the top of chat_plan.go), 2 and 3 are driven here:
//   - the refresh button re-derives the plan from the recent conversation (pressed right
//     after the plan moved during a discussion);
//   - edit / clear is the last resort for fixing what the automatic update missed or
//     overwrote.
//
// Verbatim carry-forward can fail by resurrecting an old plan, word for word and forcefully,
// over a newer agreement. The edit affordance is the only remedy for that, so the panel can
// be collapsed but not removed: the header button is always present on a conversation that
// has a plan.
import { useEffect, useState } from "react";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { chatSetPlan, chatRefreshPlan } from "./api.ts";
import { errText } from "../../core/api/client.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import type { Conversation } from "../../types/chat.ts";

interface ChatPlanProps {
  conversationId: string;
  plan: string;
  updatedAt?: number;
  /** Blocks operations that would wait on the conversation lock: a turn running, a compaction
   * in progress, or a stopped workspace. */
  disabled?: boolean;
  onUpdated: (conv: Conversation) => void;
  onClose: () => void;
}

export function ChatPlan({ conversationId, plan, updatedAt, disabled, onUpdated, onClose }: ChatPlanProps) {
  const tr = useT();
  const askConfirm = useConfirm();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(plan);
  const [busy, setBusy] = useState<"" | "refresh" | "save">("");
  const [error, setError] = useState("");

  // Follow the plan when another pane, a compaction or an automatic update moves it — except
  // while editing, where wiping what the user is typing does more harm; the save then wins.
  useEffect(() => {
    if (!editing) setDraft(plan);
  }, [plan, editing]);

  const run = async (kind: "refresh" | "save", call: () => Promise<Conversation & { error?: unknown }>) => {
    setBusy(kind);
    setError("");
    try {
      const c = await call();
      if (c && c.id) {
        onUpdated(c);
        setEditing(false);
      } else {
        setError(c?.error ? errText(c.error) : tr("chat.plan.failed"));
      }
    } catch {
      setError(tr("chat.plan.failed"));
    } finally {
      setBusy("");
    }
  };

  const doClear = async () => {
    if (
      !(await askConfirm({
        title: tr("chat.plan.clear_confirm_title"),
        body: tr("chat.plan.clear_confirm_body"),
        confirmLabel: tr("chat.plan.clear"),
        danger: true,
      }))
    )
      return;
    void run("save", () => chatSetPlan(conversationId, ""));
  };

  const working = !!busy;
  return (
    <section className="chat-plan" aria-label={tr("chat.plan.title")}>
      <header className="cp-head">
        <span className="cp-title">
          <Icon name="checklist" /> {tr("chat.plan.title")}
        </span>
        {updatedAt ? <span className="cp-ts">{fmtDateTime(updatedAt)}</span> : null}
        <span className="cp-actions">
          {editing ? (
            <>
              <button type="button" className="cp-btn" disabled={working} onClick={() => void run("save", () => chatSetPlan(conversationId, draft))}>
                <Icon name={busy === "save" ? "loading" : "check"} spin={busy === "save"} />
                {tr("common.save")}
              </button>
              <button type="button" className="cp-btn" disabled={working} onClick={() => { setEditing(false); setDraft(plan); }}>
                {tr("common.cancel")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                className="cp-btn"
                disabled={working || disabled}
                title={tr("chat.plan.refresh_tip")}
                onClick={() => void run("refresh", () => chatRefreshPlan(conversationId))}
              >
                <Icon name={busy === "refresh" ? "loading" : "refresh"} spin={busy === "refresh"} />
                {busy === "refresh" ? tr("chat.plan.refreshing") : tr("chat.plan.refresh")}
              </button>
              <button type="button" className="cp-btn" disabled={working || disabled} onClick={() => setEditing(true)}>
                <Icon name="edit" /> {tr("chat.plan.edit")}
              </button>
              {plan ? (
                <button type="button" className="cp-btn" disabled={working || disabled} onClick={() => void doClear()}>
                  <Icon name="trash" /> {tr("chat.plan.clear")}
                </button>
              ) : null}
            </>
          )}
          <button type="button" className="cp-btn cp-close" title={tr("common.close")} onClick={onClose}>
            <Icon name="chevron-up" />
          </button>
        </span>
      </header>
      {error && (
        <div className="chat-error" role="alert">
          {error}
        </div>
      )}
      {editing ? (
        <textarea
          className="cp-edit"
          value={draft}
          spellCheck={false}
          placeholder={tr("chat.plan.placeholder")}
          onChange={(e) => setDraft(e.target.value)}
        />
      ) : plan ? (
        <div className="cp-body">
          <MarkdownView source={plan} />
        </div>
      ) : (
        <p className="cp-empty muted">{tr("chat.plan.empty")}</p>
      )}
      <p className="cp-note muted">{tr("chat.plan.note")}</p>
    </section>
  );
}
