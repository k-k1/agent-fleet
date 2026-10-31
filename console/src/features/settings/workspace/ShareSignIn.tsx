// ShareSignIn — "Share one sign-in" on selected ungrouped SSO profiles (ADR 0125 decision 6).
//
// Writes through the existing PUT /api/ssm/profiles/{id}, one profile at a time, each naming the
// sign-in: the first one creates it from the profile's portal, the rest join it. There is no
// all-or-nothing call, so a run can end half done; the dialog then stays open and lists every
// profile as moved or not, with the refusal's code, and Retry moves only the rest. A half group
// is never left unannounced.
//
// What a request carries is the profile's own fields (groupBody) and the sign-in's name; no
// token, cache content or other row is read.
import { useRef, useState } from "react";
import { rawJSON } from "../../../core/api/client.ts";
import { Modal } from "../../../ui/Modal.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { Field, Meta } from "../parts/mcpForm.tsx";
import {
  groupBody,
  nameProblem,
  proposeName,
  signInSaveError,
  writeOrder,
  type GroupCandidate,
  type NameProblem,
  type SsmSignIn,
} from "./ssmSignIns.ts";

type Outcome = { status: "joined" | "failed" | "unknown" | "not_run"; code?: string };

const PROBLEM_KEYS: Record<NameProblem, MsgKey> = {
  empty: "ssm.share_p_empty",
  invalid: "ssm.share_p_invalid",
  mismatch: "ssm.share_p_mismatch",
  taken: "ssm.share_p_taken",
};

export function ShareSignIn({
  members,
  others,
  signIns,
  live,
  signInLive,
  onChanged,
  onClose,
}: {
  /** The selected profiles: ungrouped, SSO, one start URL and SSO region. */
  members: GroupCandidate[];
  /** The member's other ungrouped SSO profiles (a sign-in may not take one's name). */
  others: { name: string }[];
  signIns: SsmSignIn[];
  /** Whether the profile of that workspace name is signed in now. */
  live: (profileName: string) => boolean;
  /** Whether the sign-in of that name is signed in now. */
  signInLive: (signInName: string) => boolean;
  /** Called after every run with the profiles that joined, and whether the group kept a login. */
  onChanged: (joined: string[], keepsLogin: boolean) => void;
  onClose: () => void;
}) {
  const tr = useT();
  // Fixed at open: the rows may move to a sign-in while the dialog still lists them.
  const [group] = useState(members);
  const [name, setName] = useState(() => proposeName(members, live));
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<[number, number] | null>(null);
  const [outcomes, setOutcomes] = useState<Record<string, Outcome>>({});
  // Whether the group took over a login that works now, decided when the first run starts: a
  // later reload moves the states under it, and a retry must not reverse the answer.
  const keeps = useRef<boolean | null>(null);
  const joinedCount = group.filter((p) => outcomes[p.id]?.status === "joined").length;
  const ran = Object.keys(outcomes).length > 0;
  // After a run the name is fixed: the sign-in exists (or the first write failed and the member
  // may still change it).
  const nameLocked = busy || joinedCount > 0;
  const problem = nameProblem(
    name.trim(),
    group,
    others.filter((o) => !group.some((g) => g.name === o.name)),
    signIns,
  );
  const named = group.find((p) => p.name === name.trim());
  const existing = signIns.find((si) => si.name === name.trim());

  const run = async () => {
    const signIn = name.trim();
    if (problem || busy) return;
    const todo = writeOrder(
      group.filter((p) => outcomes[p.id]?.status !== "joined"),
      signIn,
    );
    if (keeps.current === null) {
      keeps.current = existing ? signInLive(signIn) : !!named && !named.nameCollides && live(named.name);
    }
    setBusy(true);
    const next: Record<string, Outcome> = { ...outcomes };
    const joined: string[] = [];
    // Until one write has landed the sign-in may not exist: if that first write fails, the others
    // would fail the same way, so the run stops with nothing changed.
    let created = !!existing || joinedCount > 0;
    let stopped = false;
    try {
      for (let i = 0; i < todo.length; i++) {
        const p = todo[i];
        if (stopped) {
          next[p.id] = { status: "not_run" };
          continue;
        }
        setProgress([i, todo.length]);
        let res: Response;
        try {
          res = await rawJSON(`api/ssm/profiles/${encodeURIComponent(p.id)}`, "PUT", groupBody(p, signIn));
        } catch {
          next[p.id] = { status: "unknown" };
          stopped = true;
          continue;
        }
        if (res.ok) {
          next[p.id] = { status: "joined" };
          joined.push(p.id);
          created = true;
          continue;
        }
        const j = await res.json().catch(() => null);
        next[p.id] = { status: "failed", code: typeof j?.error?.code === "string" ? j.error.code : String(res.status) };
        if (!created) stopped = true;
      }
    } finally {
      setOutcomes(next);
      setBusy(false);
      setProgress(null);
      if (joined.length > 0) onChanged(joined, keeps.current === true);
    }
  };

  const failed = group.filter((p) => outcomes[p.id] && outcomes[p.id].status !== "joined");
  const outcomeText = (p: GroupCandidate): string => {
    const o = outcomes[p.id];
    if (!o) return "";
    const known = o.status === "failed" ? signInSaveError(o.code) : null;
    return (
      tr(("ssm.share_r_" + o.status) as MsgKey, { label: p.label, name: name.trim(), code: o.code ?? "" }) + (known ? " " + tr(known) : "")
    );
  };

  return (
    <Modal title={tr("ssm.share_title")} onClose={() => !busy && onClose()} lockClose={busy} className="ssm-share">
      <div className="ui-modal-body">
        <p className="field-help">{tr("ssm.share_intro")}</p>
        <ul className="ssm-list ssm-share-members">
          {group.map((p) => (
            <li key={p.id} className="ssm-item" data-profile={p.label}>
              <span className="ssm-alias">{p.label}</span>
              {outcomes[p.id] && <span className={"ssm-share-outcome " + outcomes[p.id].status}> {outcomeText(p)}</span>}
            </li>
          ))}
        </ul>
        <div className="ssm-meta">
          <Meta k={tr("ssm.meta_sso_region")} v={group[0].ssoRegion} />
          <Meta k="start URL" v={group[0].startUrl} wide />
        </div>
        <Field label={tr("ssm.f_sign_in_name")} req hint={tr("ssm.f_sign_in_name_hint")}>
          <input
            className="cinput ssm-share-name"
            value={name}
            disabled={nameLocked}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        {problem && name !== "" && (
          <p className="ssm-load-err ssm-share-problem" role="alert">
            {tr(PROBLEM_KEYS[problem])}
          </p>
        )}
        {!problem && !ran && (
          <p className="field-help ssm-share-note">
            {existing
              ? tr(signInLive(existing.name) ? "ssm.share_joins_live" : "ssm.share_joins_out", { name: existing.name })
              : named && !named.nameCollides && live(named.name)
                ? tr("ssm.share_keeps", { name: named.name })
                : tr("ssm.share_fresh")}
          </p>
        )}
        {ran && (
          <p className={"ssm-share-summary" + (failed.length ? " warn-text" : " field-help")} role="status">
            {failed.length
              ? tr("ssm.share_partial", { done: joinedCount, total: group.length, name: name.trim() })
              : tr("ssm.share_done", { total: group.length, name: name.trim() })}
          </p>
        )}
        <div className="flow">
          {(!ran || failed.length > 0) && (
            <button type="button" className="primary ssm-share-do" disabled={busy || !!problem} onClick={() => void run()}>
              {progress
                ? tr("ssm.share_running", { done: progress[0], total: progress[1] })
                : ran
                  ? tr("ssm.share_retry", { n: failed.length })
                  : tr("ssm.share_do", { n: group.length })}
            </button>
          )}
          <button type="button" className="ghost" disabled={busy} onClick={onClose}>
            {tr("common.close")}
          </button>
        </div>
      </div>
    </Modal>
  );
}
