// The LENDING side of ADR 0079: the operator of the deployment that owns the engines mints the
// `afei_…` issuing token a borrowing deployment puts in `AF_REMOTE_ENGINE_TOKEN`, and reads it
// off this screen (control-plane/engine_issue_token.go).
//
// Why this panel exists. Decision 3 makes the credential a purpose-made membership's issuing
// token, and then the review found the procedure did not close: the value is injected into that
// membership's own workspace container and nowhere else, and no admin route starts another
// member's workspace — so reading it meant inviting a real account to the far IdP, signing in as
// it once and reading an environment variable (open question 6, and steps 1-4 of the operator
// chapter). This panel replaces all four steps with one press.
//
// 🔴 What it must never become is a way to hand out a PERSON's issuing token. The value is
// derived deterministically from this deployment's signing master, so there is no such thing as
// revoking one copy of it: the only lever is rotating that master, which is shared with the git,
// memo and schedule tokens, and would log the whole fleet out. The control plane cannot refuse
// this — "is this membership a person?" has no truthful column — so the whole prohibition is
// carried by what this screen SAYS: what the token is, what it opens, what revoking it means,
// and `has_workspace`, the one honest signal that somebody may be behind it.
//
// The warning and the revocation instruction are therefore not decoration and not optional: a
// panel that showed the token without them is the panel that gets an operator's own token pasted
// into a borrowing deployment, and the fleet held hostage by it.
//
// 🔴 Both are drawn from the answer's STRUCTURED fields (`deterministic`, `has_workspace`,
// `env_var`, `opens`, the membership itself) rather than from its `warning` / `revoke` prose.
// The CP composes those in English only — it serves one deployment, not one reader — and English
// paragraphs in the middle of a Japanese screen are the paragraphs nobody reads, which for these
// two sentences is the whole failure. The Go file states the same split: `deterministic` and
// `has_workspace` are described there as the machine-readable halves of the prose.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";

/** What `POST /api/admin/engines/issue-token` answers (engineIssueTokenView). Everything after
 *  the membership is optional here: a control plane too old to send a field must leave this panel
 *  saying LESS, never saying something it was not told. */
export type IssuedEngineToken = {
  token: string;
  membership_id: string;
  tenant_slug: string;
  user_key: string;
  role?: string;
  /** Where the borrowing deployment puts the value (decision 2). Named by the CP so this screen
   *  does not have to know the borrower's variable names — and not defaulted here for the same
   *  reason: an invented variable name is a credential pasted into nothing. */
  env_var?: string;
  /** Every route this token reaches on this deployment. Short on purpose: no git, no MCP, no
   *  memos, no API. */
  opens?: string[];
  /** The machine-readable half of the warning: the reason there is no "revoke this token"
   *  button anywhere is that there is no such operation. */
  deterministic?: boolean;
  /** This membership has a workspace — what a person's membership looks like, and the state
   *  closest to what decision 3 forbids. Reported by the CP, never enforced. */
  has_workspace?: boolean;
  /** The CP's own English prose. Kept in the type because it is in the contract; deliberately
   *  not rendered — see the file header. */
  revoke?: string;
  warning?: string;
};

/** How long the value stays on screen. A credential that is read once and then sits in a panel
 *  behind whatever the operator does next is a credential in the next screenshot and on the next
 *  shoulder; and because the mint is deterministic, dropping it costs nothing — pressing the
 *  button again returns the very same string rather than a new one. */
const ISSUE_CLEAR_MS = 120_000;

/** Not the token. Fixed width, so the row does not resize when it is revealed. */
const MASK = "••••••••••••••••";

export function EngineIssueTokenPanel() {
  const tr = useT();
  const [slug, setSlug] = useState("");
  const [userKey, setUserKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [issued, setIssued] = useState<IssuedEngineToken | null>(null);
  // Masked on arrival, revealed on request. The value is never in the document while hidden —
  // it lives in this state and nowhere else — so a screen share of this panel shows the dots.
  const [shown, setShown] = useState(false);

  const drop = () => {
    setIssued(null);
    setShown(false);
  };

  // Takes itself off the screen. Re-armed on every new answer (the state object is the
  // dependency), so pressing the button again restarts the two minutes rather than inheriting
  // what was left of the last one.
  useEffect(() => {
    if (!issued) return;
    const t = setTimeout(() => {
      setIssued(null);
      setShown(false);
    }, ISSUE_CLEAR_MS);
    return () => clearTimeout(t);
  }, [issued]);

  const issue = async (ev: FormEvent) => {
    ev.preventDefault();
    const t = slug.trim();
    const k = userKey.trim();
    if (!t || !k) return;
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/issue-token", "POST", {
        tenant_slug: t,
        user_key: k,
      });
      if (d?.error) {
        // errDetail, not errText: `membership_inactive` and the two 404s all have a translation
        // for WHAT went wrong, and the CP's message is the only place the rest of the sentence
        // is ("restore the membership first", which tenant it looked in).
        setErr(errDetail(d.error));
        drop();
        return;
      }
      setErr("");
      setIssued(d as IssuedEngineToken);
      setShown(false);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel engines-issue">
      <div className="usage-toolbar">
        <span>{tr("admin.engines_issue_head")}</span>
      </div>
      <p className="muted">{tr("admin.engines_issue_intro")}</p>
      {/* What pressing the button does, BEFORE it is pressed. The distinction that has to survive
          is "it shows the membership's existing credential" rather than "it creates one for this
          borrower": an operator who believes the second one issues a second token to cut the
          first off, and there is no second token. */}
      <p className="muted">{tr("admin.engines_issue_before")}</p>
      <p className="form-err">{tr("admin.engines_issue_before_only")}</p>
      <form onSubmit={issue}>
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_issue_tenant")}</span>
          <input
            type="text"
            value={slug}
            placeholder={tr("admin.engines_issue_tenant_ph") as string}
            onChange={(ev) => setSlug(ev.currentTarget.value)}
          />
        </label>
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_issue_user_key")}</span>
          <input
            type="text"
            value={userKey}
            placeholder={tr("admin.engines_issue_user_key_ph") as string}
            onChange={(ev) => setUserKey(ev.currentTarget.value)}
          />
        </label>
        <div className="engines-model-add-actions">
          <button type="submit" className="btn-secondary" disabled={busy || !slug.trim() || !userKey.trim()}>
            {busy ? tr("admin.engines_issue_working") : tr("admin.engines_issue_submit")}
          </button>
        </div>
      </form>
      {err && <p className="form-err">{err}</p>}
      {issued && <IssuedToken view={issued} shown={shown} onShown={setShown} onDrop={drop} />}
    </section>
  );
}

/** The answer. Read top to bottom it is one statement: whose token this is, whether somebody may
 *  be behind that membership, the value, where it goes, what it opens, and how it is taken away.
 *
 *  🔴 The order is deliberate. `has_workspace` comes BEFORE the value: it is the one thing that
 *  should stop the operator from copying what is underneath it, and a caution under a credential
 *  is read after the credential is already on the clipboard. */
function IssuedToken({
  view,
  shown,
  onShown,
  onDrop,
}: {
  view: IssuedEngineToken;
  shown: boolean;
  onShown: (v: boolean) => void;
  onDrop: () => void;
}) {
  const tr = useT();
  const opens = view.opens || [];
  return (
    <div className="engines-issue-result">
      <p className="engines-issue-for">
        {tr("admin.engines_issue_for", {
          t: view.tenant_slug,
          k: view.user_key,
          role: view.role || "",
          id: view.membership_id,
        })}
      </p>
      {/* The closest thing to evidence that this membership belongs to a person (decision 3's 🔴).
          The CP does not refuse it — there is no column that could answer honestly — so the whole
          weight of the prohibition is on how loudly this is said. */}
      {view.has_workspace ? (
        <div className="engines-issue-person">
          <span className="engines-model-tag bad">{tr("admin.engines_issue_has_workspace_tag")}</span>
          <p className="form-err">{tr("admin.engines_issue_has_workspace")}</p>
        </div>
      ) : (
        <p className="muted">{tr("admin.engines_issue_no_workspace")}</p>
      )}
      <div className="engines-issue-value">
        <span className="mono engines-issue-token">{shown ? view.token : MASK}</span>
        <button type="button" className="sm" onClick={() => onShown(!shown)}>
          {tr(shown ? "admin.engines_issue_hide" : "admin.engines_issue_reveal")}
        </button>
        {/* Copying does not need it revealed: the value is in state, and the point of the mask is
            that reading it with the eyes is the rarer of the two things done with it. */}
        <CopyButton text={view.token} label={tr("admin.engines_issue_copy")} />
        {/* The assignment line, ready to paste into wherever the borrower keeps its AF_* secrets.
            Only where the CP NAMED the variable — an invented name is a credential pasted into
            nothing. */}
        {view.env_var && (
          <CopyButton
            text={view.env_var + "=" + view.token}
            label={tr("admin.engines_issue_copy_env", { v: view.env_var })}
          />
        )}
        <button type="button" className="sm" onClick={onDrop}>
          {tr("admin.engines_issue_clear")}
        </button>
      </div>
      {view.env_var && (
        <p className="engines-issue-env">
          <span className="engines-fact-label">{tr("admin.engines_issue_env_label")}</span>
          <span className="mono engines-model-tag">{view.env_var}</span>
        </p>
      )}
      <p className="muted">
        {tr("admin.engines_issue_autoclear", { m: String(Math.round(ISSUE_CLEAR_MS / 60000)) })}
      </p>
      {opens.length > 0 && (
        <p className="engines-issue-opens">
          <span className="engines-fact-label">{tr("admin.engines_issue_opens_label")}</span>
          {opens.map((o) => (
            <span key={o} className="mono engines-model-tag">
              {o}
            </span>
          ))}
        </p>
      )}
      {opens.length > 0 && <p className="muted">{tr("admin.engines_issue_opens_only")}</p>}
      {/* 🔴 Shown unless the CP says the value is NOT deterministic. Not "shown when
          deterministic is true": a control plane that omitted the field would then serve a
          credential with the one sentence that explains why it cannot be taken back missing. */}
      {view.deterministic !== false && (
        <p className="form-err">{tr("admin.engines_issue_deterministic")}</p>
      )}
      <p className="engines-issue-revoke">
        <span className="engines-fact-label">{tr("admin.engines_issue_revoke_label")}</span>
        {tr("admin.engines_issue_revoke", { t: view.tenant_slug, k: view.user_key })}
      </p>
      {/* The price of the alternative, in the same breath. Without it "delete the membership"
          reads as one option among several, and the other one costs every git, memo and schedule
          token in this deployment. */}
      <p className="form-err">{tr("admin.engines_issue_revoke_only")}</p>
    </div>
  );
}

/** Copy, with the acknowledgement that it happened. Same shape as the chat's copy button: the
 *  clipboard is unavailable in an insecure context, and a button that silently does nothing there
 *  is worse than one that stays unpressed. */
function CopyButton({ text, label }: { text: string; label: string }) {
  const tr = useT();
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
    <button type="button" className="sm" onClick={copy}>
      {done ? tr("admin.engines_issue_copied") : label}
    </button>
  );
}
