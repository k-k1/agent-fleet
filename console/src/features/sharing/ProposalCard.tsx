import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";

export interface Proposal {
  id: string;
  sessionId: string;
  sessionName?: string;
  sessionTitle?: string;
  proposerUserKey: string;
  proposerEmail?: string;
  action: string;
  payload?: Record<string, unknown> | null;
  status: string;
  refused?: string;
  expiresAt: string;
}

// Fields the card draws itself. Anything else the payload carries is listed by name under
// "other", so a field is never relayed without being on screen.
const KNOWN = new Set(["op", "prompt", "feedback", "attachments", "scope", "decision", "id", "clientMessageID", "choices", "answers"]);

interface Answer { text?: string; options?: number[] }

const text = (v: unknown) => (typeof v === "string" ? v : v == null ? "" : JSON.stringify(v));

// What approval will send to the owner's Agent: target session, action, the text, every
// attachment and the reply scope. The proposer is named by login id, as the rest of the
// sharing UI does.
export function ProposalCard({ p, onDecide }: { p: Proposal; onDecide: (id: string, d: "approve" | "reject") => void }) {
  const tr = useT();
  const d = p.payload ?? {};
  const attachments = Array.isArray(d.attachments) ? d.attachments : d.attachments == null ? [] : [d.attachments];
  const body = text(d.prompt) || text(d.feedback);
  // A proposal stored before the allow-list may hold anything here: each element is checked,
  // and one that is not a well-formed answer is shown as plain JSON text instead.
  const answers: (Answer | string)[] = (Array.isArray(d.answers) ? d.answers : []).map((a) => {
    const o = a as { text?: unknown; options?: unknown } | null;
    const ok = o !== null && typeof o === "object" && !Array.isArray(o)
      && (o.text === undefined || typeof o.text === "string")
      && (o.options === undefined || (Array.isArray(o.options) && o.options.every((n) => typeof n === "number")));
    return ok ? (o as Answer) : text(a);
  });
  const choices = Array.isArray(d.choices) ? d.choices : [];
  const other = Object.keys(d).filter((k) => !KNOWN.has(k));
  const processing = p.status === "processing";
  return (
    <article className="share-proposal-card">
      <strong>{p.proposerEmail || p.proposerUserKey}</strong>
      <dl className="share-proposal-meta">
        <dt>{tr("share.proposal_session")}</dt>
        <dd>{p.sessionTitle || p.sessionName || p.sessionId}{p.sessionName && p.sessionTitle ? ` (${p.sessionName})` : ""}</dd>
        <dt>{tr("share.proposal_action")}</dt>
        <dd>{p.action}{d.op ? ` / ${text(d.op)}` : ""}{d.decision ? ` / ${text(d.decision)}` : ""}</dd>
        {d.scope != null && <><dt>{tr("share.proposal_scope")}</dt><dd>{text(d.scope)}</dd></>}
        {attachments.length > 0 && (
          <>
            <dt>{tr("share.proposal_attachments")}</dt>
            <dd><ul className="share-proposal-attachments">{attachments.map((a, i) => <li key={i}><code>{text(a)}</code></li>)}</ul></dd>
          </>
        )}
        {d.id != null && <><dt>{tr("share.proposal_interaction")}</dt><dd><code>{text(d.id)}</code></dd></>}
        {d.clientMessageID != null && <><dt>{tr("share.proposal_message_id")}</dt><dd><code>{text(d.clientMessageID)}</code></dd></>}
        {choices.length > 0 && <><dt>{tr("share.proposal_choices")}</dt><dd>{choices.map(text).join(", ")}</dd></>}
        {answers.length > 0 && (
          <>
            <dt>{tr("share.proposal_answers")}</dt>
            <dd>
              <ol className="share-proposal-attachments">
                {answers.map((a, i) => (
                  <li key={i}>
                    {typeof a === "string" ? <code>{a}</code> : (
                      <>
                        {a.text ? <span>{a.text}</span> : null}
                        {a.options && a.options.length > 0 && <span> [{a.options.join(", ")}]</span>}
                      </>
                    )}
                  </li>
                ))}
              </ol>
            </dd>
          </>
        )}
        {other.length > 0 && <><dt>{tr("share.proposal_other")}</dt><dd>{other.join(", ")}</dd></>}
      </dl>
      {body && <p>{body}</p>}
      {p.refused && <p className="muted" role="alert">{tr("share.proposal_refused")}</p>}
      {processing && <p className="muted">{tr("share.outcome_unknown")}</p>}
      <div className="ui-modal-actions">
        {p.status === "pending" && <Button onClick={() => onDecide(p.id, "reject")}>{tr("share.reject")}</Button>}
        {processing
          ? <Button onClick={() => onDecide(p.id, "approve")}>{tr("share.reconcile")}</Button>
          : !p.refused && <Button variant="primary" onClick={() => onDecide(p.id, "approve")}>{tr("share.approve")}</Button>}
      </div>
    </article>
  );
}
