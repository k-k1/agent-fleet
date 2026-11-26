// HandoffOfferModal is where an owner offers "carry this on" to one of the people they
// share with (docs/log/77 / ADR 0057).
//
// The recipient candidates come from a reverse lookup of the share ACL, never the tenant
// directory — hence a plain select rather than the search combobox of ShareCreateModal:
// only people already chosen to see this session can appear.
//
// The push gate is a fact the CP obtained from the owner's Agent and is only displayed
// here. Recomputing the decision client-side would split the condition across two places
// and it would drift from the server's check at send time (docs/log/77 §77.5). The same holds
// for "push, then send" (#2072): the CP asks the Agent to push and then for a fresh verdict;
// this surface only asks for it.
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, errText } from "../../core/api/client.ts";
import { tCount, useT } from "../../lib/i18n/index.ts";
import { SESSION_TITLE_MAX, clampSessionTitle } from "../../lib/sessionTitle.ts";
import { ShareCreateModal } from "./ShareCreateModal.tsx";
import { useHandoffStore } from "./handoffStore.ts";
import { SHARE_REQUEST_TIMEOUT_MS, boundedRequest, type BoundedRequest } from "./boundedRequest.ts";
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
  /** The session runs in a space: its files and attachments do not travel. */
  space?: boolean;
  /** Where "push, then send" would publish (the push URL, credentials removed) and how many
   *  commits; set only when a push can clear the gate and lands where the offer points. */
  pushRemote?: string;
  pushCommits?: number;
}

/** The gate reason arrives as a machine token; the wording is resolved here so the server
 *  carries no display text. */
const BLOCKED_KEYS = {
  unpushed_commits: "handoff.blocked_unpushed",
  no_upstream: "handoff.blocked_no_upstream",
  detached_head: "handoff.blocked_detached",
} as const;

/** The gate reasons a push clears. The Agent sets pushRemote only for these, so this guards an
 *  older Agent rather than deciding anything. */
const PUSHABLE = new Set(["unpushed_commits", "no_upstream"]);

function blockedKey(reason: string): (typeof BLOCKED_KEYS)[keyof typeof BLOCKED_KEYS] | "handoff.blocked_unknown" {
  return BLOCKED_KEYS[reason as keyof typeof BLOCKED_KEYS] ?? "handoff.blocked_unknown";
}

// HANDOFF_PUSH_BOUND_MS is the Agent's own bound on "push, then send" (handoffPushTimeout
// 60 s plus handoffPushWaitDelay 2 s, workspace/agent/internal/sessionx/session_handoff_push.go).
// A send that pushes gets it on top of the ordinary deadline, so the Console never abandons
// a push the Agent is still allowed to finish.
const HANDOFF_PUSH_BOUND_MS = 62_000;

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
  // The verdict is a second request (the Agent runs git for it), so the recipients show
  // first. Until it has answered there is no verdict, and the send button stays disabled.
  const [ctxReady, setCtxReady] = useState(false);
  const [listErr, setListErr] = useState("");
  const [ctxErr, setCtxErr] = useState("");
  const loadErr = listErr || ctxErr;
  const [recipient, setRecipient] = useState("");
  // This becomes the session name verbatim in the recipient's workspace, so clamp it to
  // the creation API's rules when offering: sending an over-long name makes the failure
  // land on the recipient.
  const [title, setTitle] = useState(() => clampSessionTitle(initialTitle || ""));
  const [prompt, setPrompt] = useState(initialPrompt);
  // The warning the owner ticked. A tick belongs to that warning alone: when the verdict changes
  // to another one (uncommitted changes, then the space note), it no longer counts and the box
  // shows unticked, so a warning is never acknowledged unread.
  const [ackedFor, setAckedFor] = useState("");
  const [pushFirst, setPushFirst] = useState(false);
  // git's own report of a refused push. Multi-line and the thing to act on, so it stays in
  // the modal rather than in a toast that fades.
  const [pushErr, setPushErr] = useState("");
  const [sending, setSending] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);

  // The recipient lookup and the send both wait on the owner's Workspace (catalog sync,
  // handoff-context, the push), so each is bounded and abandoned when superseded or closed.
  const loadReq = useRef<BoundedRequest | null>(null);
  const sendReq = useRef<BoundedRequest | null>(null);
  const load = () => {
    setLoading(true);
    setCtxReady(false);
    loadReq.current?.cancel();
    const req = boundedRequest();
    loadReq.current = req;
    // Only the newest lookup may write: an older one answering late would show another
    // session's recipients.
    const current = () => loadReq.current === req;
    const listP = api(`api/sessions/${encodeURIComponent(session)}/handoff-recipients?context=skip`, { signal: req.signal })
      .then((d) => {
        if (!current()) return;
        if (d?.error) {
          setListErr(errText(d.error));
          setMembers([]);
          return;
        }
        setListErr("");
        setMembers(Array.isArray(d.members) ? d.members : []);
        setRecipient((prev) => prev || (d.members?.[0]?.userKey ?? ""));
      })
      .catch(() => {
        if (current()) setListErr(tr("handoff.load_failed"));
      })
      .finally(() => {
        if (current()) setLoading(false);
      });
    const ctxP = api(`api/sessions/${encodeURIComponent(session)}/handoff-context`, { signal: req.signal })
      .then((d) => {
        if (!current()) return;
        if (d?.error) {
          setCtxErr(errText(d.error));
          return;
        }
        setCtxErr("");
        setCtx((d.context || null) as HandoffContext | null);
        // A refreshed verdict is a new snapshot: the push the owner confirmed was for the
        // previous one, so the box starts unticked again.
        setPushFirst(false);
        setCtxReady(true);
      })
      .catch(() => {
        if (current()) setCtxErr(tr("handoff.load_failed"));
      });
    void Promise.allSettled([listP, ctxP]).then(() => req.settle());
  };
  useEffect(load, [session]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => {
    for (const ref of [loadReq, sendReq]) {
      const req = ref.current;
      ref.current = null;
      req?.cancel();
    }
  }, []);

  const blocked = ctx?.blocked || "";
  const warning = ctx?.warning || "";
  const ackWarning = !!warning && ackedFor === warning;
  const pushable = PUSHABLE.has(blocked) && !!ctx?.pushRemote;
  const pushing = pushable && pushFirst;
  const canSubmit =
    ctxReady &&
    !!recipient && !!title.trim() && !!prompt.trim() && (!blocked || pushing) && (!warning || ackWarning);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || sending) return;
    setSending(true);
    const req = boundedRequest(SHARE_REQUEST_TIMEOUT_MS + (pushing ? HANDOFF_PUSH_BOUND_MS : 0));
    sendReq.current = req;
    const d = await api("api/session-handoff-offers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        sessionName: session,
        recipientUserKey: recipient,
        title: title.trim(),
        prompt,
        ackWarning,
        ackedWarning: ackWarning ? warning : "",
        pushFirst: pushing,
        // What the owner saw next to the box. The CP pushes only while the working copy still
        // matches it, exactly that commit, and sends only if the state after the push does too.
        ...(pushing && { pushExpect: { branch: ctx?.branch, headSha: ctx?.headSha, pushRemote: ctx?.pushRemote } }),
      }),
      signal: req.signal,
    }).catch(() => (req.signal.aborted && !req.timedOut() ? null : { error: { message: tr(req.timedOut() ? "handoff.send_timeout" : "handoff.send_failed") } }));
    req.settle();
    if (sendReq.current !== req || d === null) return; // closed meanwhile
    sendReq.current = null;
    setSending(false);
    if (d?.error?.code === "handoff_push_failed") {
      setPushErr(String(d.error.message || ""));
      load();
      return;
    }
    setPushErr("");
    if (d?.error) {
      // The verdict changed after the box was ticked: the screen below is refetched, so point at
      // it in the member's language instead of the server's English.
      toast(d.error.code === "handoff_warning" ? tr("handoff.warning_changed") : errText(d.error));
      // A timed-out send may still have created the offer; resending is safe (an offer
      // already pending answers 409).
      if (req.timedOut()) void useHandoffStore.getState().refresh();
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
  // Closing abandons a send in flight instead of waiting for it.
  const close = () => {
    const req = sendReq.current;
    if (req) {
      sendReq.current = null;
      req.cancel();
      void useHandoffStore.getState().refresh();
    }
    onClose();
  };

  return (
    <>
      <Modal
        title={tr("handoff.offer_title")}
        onClose={close}
        as="form"
        onSubmit={submit}
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
              {ctx?.space && <p className="ui-field-hint handoff-blocked">{tr("handoff.space_hint")}</p>}
              {!ctxReady && <p className="ui-field-hint">{tr("handoff.gate_checking")}</p>}
              {blocked && <p className="ui-field-hint handoff-blocked">{tr(blockedKey(blocked))}</p>}
              {pushable && (
                <label className="ui-field-inline handoff-push">
                  <input type="checkbox" checked={pushFirst} onChange={(e) => setPushFirst(e.target.checked)} />
                  <span>
                    {tr("handoff.push_then_send")}
                    <span className="ui-field-hint handoff-push-summary">
                      {tCount("handoff.push_summary", ctx?.pushCommits ?? 0, {
                        branch: ctx?.branch || "-",
                        remote: ctx?.pushRemote || "-",
                      })}
                    </span>
                  </span>
                </label>
              )}
              {pushErr && (
                <div className="handoff-blocked handoff-push-error" role="alert">
                  <p className="ui-field-hint">{tr("handoff.push_failed")}</p>
                  <pre>{pushErr}</pre>
                </div>
              )}
              {(!blocked || pushing) && warning && (
                <label className="ui-field-inline">
                  <input type="checkbox" checked={ackWarning} onChange={(e) => setAckedFor(e.target.checked ? warning : "")} />
                  {tr(warning === "space_files_stay" ? "handoff.ack_space" : "handoff.ack_dirty")}
                </label>
              )}
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_title_label")}</span>
                <input {...NO_AUTOFILL} value={title} maxLength={SESSION_TITLE_MAX} onChange={(e) => setTitle(e.target.value)} />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_prompt_label")}</span>
                <textarea {...NO_AUTOFILL}
                  className="handoff-prompt-edit"
                  value={prompt}
                  spellCheck={false}
                  onChange={(e) => setPrompt(e.target.value)}
                />
              </label>
              {/* The prompt is frozen once sent (ADR 0057 decision 7). Say so: silently
                  having no effect is the worst outcome. A consequence warning, so it stays
                  inline (CONVENTIONS §13). */}
              <p className="ui-field-hint">{tr("handoff.frozen_hint")}</p>
            </>
          )}
        </div>
        <div className="ui-modal-foot">
          <Button variant="ghost" onClick={close}>
            {tr("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={!canSubmit || sending}>
            {sending && pushing ? tr("handoff.pushing") : tr("handoff.send")}
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
