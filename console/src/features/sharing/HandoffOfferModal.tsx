// HandoffOfferModal is where an owner offers "carry this on" to one of the people they
// share with (docs/log/77 / ADR 0057).
//
// The recipient candidates come from a reverse lookup of the share ACL, never the tenant
// directory — hence a plain select rather than the search combobox of ShareCreateModal:
// only people already chosen to see this session can appear.
//
// The push gate is a fact the CP obtained from the owner's Agent and is only displayed
// here. Recomputing the decision client-side would split the condition across two places
// and it would drift from the server's check at send time (docs/log/77 §77.5).
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { SESSION_TITLE_MAX, clampSessionTitle } from "../../lib/sessionTitle.ts";
import { ShareCreateModal } from "./ShareCreateModal.tsx";
import { useHandoffStore } from "./handoffStore.ts";
import "./sharing.css";

interface RecipientCandidate {
  userKey: string;
  email: string;
}

/** The Agent's verdict, relayed unchanged by the CP
 *  (workspace/agent/session_handoff_context.go). */
interface HandoffContext {
  repo?: string;
  vcs?: string;
  branch?: string;
  remote?: string;
  headSha?: string;
  ahead?: number;
  dirty?: boolean;
  blocked?: string;
  warning?: string;
}

/** The gate reason arrives as a machine token; the wording is resolved here so the server
 *  carries no display text. */
const BLOCKED_KEYS = {
  unpushed_commits: "handoff.blocked_unpushed",
  no_upstream: "handoff.blocked_no_upstream",
  detached_head: "handoff.blocked_detached",
} as const;

function blockedKey(reason: string): (typeof BLOCKED_KEYS)[keyof typeof BLOCKED_KEYS] | "handoff.blocked_unknown" {
  return BLOCKED_KEYS[reason as keyof typeof BLOCKED_KEYS] ?? "handoff.blocked_unknown";
}

export function HandoffOfferModal({
  session,
  initialTitle,
  initialPrompt,
  onClose,
  onSent,
}: {
  session: string;
  initialTitle?: string;
  initialPrompt: string;
  onClose: () => void;
  onSent?: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [loading, setLoading] = useState(true);
  const [members, setMembers] = useState<RecipientCandidate[]>([]);
  const [ctx, setCtx] = useState<HandoffContext | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [recipient, setRecipient] = useState("");
  // This becomes the session name verbatim in the recipient's workspace, so clamp it to
  // the creation API's rules when offering: sending an over-long name makes the failure
  // land on the recipient.
  const [title, setTitle] = useState(() => clampSessionTitle(initialTitle || ""));
  const [prompt, setPrompt] = useState(initialPrompt);
  const [ackWarning, setAckWarning] = useState(false);
  const [sending, setSending] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);

  const load = () => {
    setLoading(true);
    api(`api/sessions/${encodeURIComponent(session)}/handoff-recipients`)
      .then((d) => {
        if (d?.error) {
          setLoadErr(errText(d.error));
          setMembers([]);
          return;
        }
        setLoadErr("");
        setMembers(Array.isArray(d.members) ? d.members : []);
        setCtx((d.context || null) as HandoffContext | null);
        setRecipient((prev) => prev || (d.members?.[0]?.userKey ?? ""));
      })
      .catch(() => setLoadErr(tr("handoff.load_failed")))
      .finally(() => setLoading(false));
  };
  useEffect(load, [session]); // eslint-disable-line react-hooks/exhaustive-deps

  const blocked = ctx?.blocked || "";
  const warning = ctx?.warning || "";
  const canSubmit = !!recipient && !!title.trim() && !!prompt.trim() && !blocked && (!warning || ackWarning);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || sending) return;
    setSending(true);
    const d = await apiJSON("api/session-handoff-offers", "POST", {
      sessionName: session,
      recipientUserKey: recipient,
      title: title.trim(),
      prompt,
      ackWarning,
    }).catch(() => ({ error: { message: tr("handoff.send_failed") } }));
    setSending(false);
    if (d?.error) {
      toast(errText(d.error));
      // Hitting the gate, or an offer already pending, can first become true here (a
      // commit landed after the modal opened). Refetch the verdict and match the screen
      // to it.
      load();
      return;
    }
    void useHandoffStore.getState().refresh();
    onSent?.();
    onClose();
  };

  return (
    <>
      <Modal
        title={tr("handoff.offer_title")}
        onClose={onClose}
        as="form"
        onSubmit={submit}
        lockClose={sending}
        className="share-create-modal"
      >
        <div className="ui-modal-body">
          <p className="ui-field-hint">{tr("handoff.offer_intro")}</p>
          {loading ? (
            <p className="ui-field-hint">{tr("common.loading")}</p>
          ) : loadErr ? (
            <p className="ui-field-hint handoff-blocked">{loadErr}</p>
          ) : members.length === 0 ? (
            <>
              {/* A handoff cannot go to someone the session is not shared with (ADR 0057
                  decision 2), so the way to create that share is offered right here rather
                  than leaving a dead end. */}
              <p className="ui-field-hint handoff-blocked">{tr("handoff.not_shared")}</p>
              <Button variant="ghost" onClick={() => setShareOpen(true)}>
                <Icon name="broadcast" /> {tr("handoff.share_first")}
              </Button>
            </>
          ) : (
            <>
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.recipient")}</span>
                <select value={recipient} onChange={(e) => setRecipient(e.target.value)}>
                  {members.map((m) => (
                    <option key={m.userKey} value={m.userKey}>
                      {m.email || m.userKey}
                    </option>
                  ))}
                </select>
              </label>
              {/* The coordinates. The recipient's disk has none of the owner's uncommitted
                  changes, so the sender is shown which commit is being handed over. */}
              {ctx?.branch && (
                <p className="ui-field-hint">
                  {tr("handoff.coordinates", {
                    branch: ctx.branch,
                    sha: (ctx.headSha || "").slice(0, 8),
                    remote: ctx.remote || "-",
                  })}
                </p>
              )}
              {blocked && <p className="ui-field-hint handoff-blocked">{tr(blockedKey(blocked))}</p>}
              {!blocked && warning && (
                <label className="ui-field-inline">
                  <input type="checkbox" checked={ackWarning} onChange={(e) => setAckWarning(e.target.checked)} />
                  {tr("handoff.ack_dirty")}
                </label>
              )}
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_title_label")}</span>
                <input value={title} maxLength={SESSION_TITLE_MAX} onChange={(e) => setTitle(e.target.value)} />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_prompt_label")}</span>
                <textarea
                  className="handoff-prompt-edit"
                  value={prompt}
                  spellCheck={false}
                  onChange={(e) => setPrompt(e.target.value)}
                />
              </label>
              {/* The prompt is frozen once sent (ADR 0057 decision 7). Say so: silently
                  having no effect is the worst outcome. */}
              <p className="ui-field-hint">{tr("handoff.frozen_hint")}</p>
            </>
          )}
        </div>
        <div className="ui-modal-foot">
          <Button variant="ghost" onClick={onClose}>
            {tr("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={!canSubmit || sending}>
            {tr("handoff.send")}
          </Button>
        </div>
      </Modal>
      {shareOpen && (
        <ShareCreateModal
          initialTarget={`session:${session}`}
          onClose={() => setShareOpen(false)}
          onCreated={() => {
            setShareOpen(false);
            load();
          }}
        />
      )}
    </>
  );
}
