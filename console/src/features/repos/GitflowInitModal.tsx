// GitflowInitModal — "Initialize Git Flow" on a repository (ADR 0103 decision 9).
//
// A git-flow declaration is usually absent from a fresh clone: git-flow keeps its keys in
// .git/config, which a clone does not copy. This writes the keys `git flow init` would, into
// the clone's config, so the resolver, the `git flow` CLI and Fork read one declaration.
//
// The config is shared by every worktree and session of the repository, so the dialog says
// so, lists every existing key it would change, and sends back the keys it opened with: the
// Agent answers 409 when someone changed them meanwhile, and the dialog reloads rather than
// overwrite what it never showed. It never switches a branch; the only one it creates is a
// local branch tracking an origin-only production or development branch, which gitflow-avh
// needs (ADR 0103 decision 9's amendment).
import { useEffect, useId, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { branchPlace, fetchGitflow, gitflowChanges, saveGitflow, upstreamCommand } from "./gitflow.ts";
import type { GitflowState, GitflowValues } from "./gitflow.ts";

interface GitflowInitModalProps {
  repo: string;
  onClose: () => void;
  /** Fired after the keys were written: a launch re-resolves its name and base. */
  onSaved?: () => void;
}

const BRANCH_FIELDS = ["production", "development"] as const;
const PREFIX_FIELDS = ["feature", "bugfix", "release", "hotfix", "versiontag"] as const;
// Enough to offer names while typing; the Agent itself sends at most 2000 per side.
const MAX_OPTIONS = 300;

export function GitflowInitModal({ repo, onClose, onSaved }: GitflowInitModalProps) {
  const tr = useT();
  const toast = useToast();
  const listId = useId();
  // undefined = loading, null = nothing to show (an older Agent, not git, Workspace stopped).
  const [st, setSt] = useState<GitflowState | null | undefined>(undefined);
  const [v, setV] = useState<GitflowValues | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ text: string; field?: string; cmds?: string[] } | null>(null);

  const load = async () => {
    const s = await fetchGitflow(repo);
    setSt(s);
    setV(s ? { ...s.prefill } : null);
    return s;
  };

  useEffect(() => {
    let alive = true;
    void fetchGitflow(repo).then((s) => {
      if (!alive) return;
      setSt(s);
      setV(s ? { ...s.prefill } : null);
    });
    return () => {
      alive = false;
    };
  }, [repo]);

  const set = (f: keyof GitflowValues) => (value: string) => {
    setV((cur) => (cur ? { ...cur, [f]: value } : cur));
    if (err?.field === f) setErr(null);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || !st || !v) return;
    setBusy(true);
    setErr(null);
    try {
      const res = await saveGitflow(repo, st.current, v);
      if (res.ok) {
        toast(
          res.created.length
            ? tr("gitflow.saved_created", { name: repo, branches: res.created.join(", ") })
            : tr("gitflow.saved", { name: repo }),
          { kind: "success" },
        );
        onSaved?.();
        onClose();
        return;
      }
      if (res.code === "gitflow_changed") {
        // What the person reviewed is no longer what would be overwritten: show the new state.
        await load();
        setErr({ text: tr("gitflow.err_changed") });
        return;
      }
      if (res.code === "branch_missing") {
        setErr({ text: tr("gitflow.err_branch_missing", { branch: res.field === "production" ? v.production : v.development }), field: res.field });
        return;
      }
      if (res.code === "invalid_value") {
        setErr({ text: tr("gitflow.err_invalid", { detail: res.message }), field: res.field });
        return;
      }
      const written = res.written ?? [];
      const created = res.created ?? [];
      const madeNote = created.length ? " " + tr("gitflow.err_created", { branches: created.join(", ") }) : "";
      if (written.length || created.length) {
        // Keys written so far changed the config: without the new state as `expected`, the
        // "save again" the message asks for would be a 409. Created branches are local now, so
        // their fields stop saying one will be made. The typed values stay.
        const s = await fetchGitflow(repo);
        if (s) setSt(s);
      }
      if (res.code === "branch_failed") {
        const untracked = res.untracked ?? [];
        // A branch left without its upstream exists now, so saving again skips it: say how to
        // finish it by hand instead of promising a retry fixes it.
        setErr(
          untracked.length
            ? {
                text: tr("gitflow.err_untracked", { err: res.message, branches: untracked.join(", ") }) + madeNote,
                cmds: untracked.map(upstreamCommand),
              }
            : { text: tr("gitflow.err_branch_failed", { err: res.message }) + madeNote },
        );
        return;
      }
      setErr({
        text:
          (written.length
            ? tr("gitflow.err_partial", { err: res.message, keys: written.join(", ") })
            : tr("gitflow.err_failed", { err: res.message })) + madeNote,
      });
    } finally {
      setBusy(false);
    }
  };

  const changes = st && v ? gitflowChanges(st, v) : [];
  const initialised = !!st && Object.keys(st.current).length > 0;
  const options = st ? [...new Set([...st.local, ...st.origin])].slice(0, MAX_OPTIONS) : [];

  const branchHint = (f: (typeof BRANCH_FIELDS)[number]) => {
    if (!st || !v || !v[f].trim()) return null;
    const name = v[f].trim();
    switch (branchPlace(st, name)) {
      case "both":
      case "local":
        return null;
      case "origin":
        return <span className="ui-field-hint gitflow-place">{tr("gitflow.origin_only", { branch: name })}</span>;
      default:
        return <span className="ui-field-hint warn gitflow-place">{tr("gitflow.missing", { branch: name })}</span>;
    }
  };

  return (
    <Modal
      title={tr("gitflow.title", { name: repo })}
      onClose={onClose}
      as="form"
      onSubmit={submit}
      lockClose={busy}
      className="gitflow-modal"
    >
      <div className="ui-modal-body">
        <p className="ui-field-hint">{tr("gitflow.intro")}</p>
        <p className="ui-field-hint warn">{tr("gitflow.shared")}</p>
        {st === undefined && <p className="ui-field-hint">{tr("common.loading")}</p>}
        {st === null && <p className="ui-field-hint svn-auth-err">{tr("gitflow.unavailable")}</p>}
        {st && v && (
          <>
            {st.committed.length > 0 && (
              <p className="ui-field-hint warn">{tr("gitflow.committed", { files: st.committed.join(", ") })}</p>
            )}
            {st.native && <p className="ui-field-hint warn">{tr("gitflow.native")}</p>}
            <datalist id={listId}>
              {options.map((b) => (
                <option key={b} value={b} />
              ))}
            </datalist>
            {BRANCH_FIELDS.map((f) => (
              <label key={f} className="ui-field">
                <span className="ui-field-label">{tr(`gitflow.field.${f}`)}</span>
                <input
                  type="text"
                  name={f}
                  value={v[f]}
                  list={listId}
                  onChange={(e) => set(f)(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={err?.field === f || undefined}
                />
                {branchHint(f)}
              </label>
            ))}
            <div className="gitflow-prefixes">
              {PREFIX_FIELDS.map((f) => (
                <label key={f} className="ui-field">
                  <span className="ui-field-label">{tr(`gitflow.field.${f}`)}</span>
                  <input
                    type="text"
                    name={f}
                    value={v[f]}
                    onChange={(e) => set(f)(e.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder={f === "bugfix" || f === "versiontag" ? tr("gitflow.optional") : undefined}
                    aria-invalid={err?.field === f || undefined}
                  />
                </label>
              ))}
            </div>
            <p className="ui-field-hint">{tr("gitflow.bugfix_hint")}</p>
            {changes.length > 0 && (
              <div className="gitflow-changes" role="status">
                <span className="ui-field-hint warn">{tr("gitflow.overwrites")}</span>
                <ul>
                  {changes.map((c) => (
                    <li key={c.key}>
                      <code>{c.key}</code>: <code>{c.from || '""'}</code> → <code>{c.to || '""'}</code>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
        {err && (
          <p className="ui-field-hint svn-auth-err gitflow-err" role="alert">
            {err.text}
            {err.cmds?.map((c) => (
              <code key={c} className="gitflow-cmd">
                {c}
              </code>
            ))}
          </p>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button type="submit" variant="primary" disabled={busy || !st || !v}>
          {busy ? tr("gitflow.saving") : changes.length > 0 ? tr("gitflow.submit_overwrite") : initialised ? tr("gitflow.submit_save") : tr("gitflow.submit")}
        </Button>
      </footer>
    </Modal>
  );
}
