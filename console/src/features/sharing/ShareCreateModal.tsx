// ShareCreateModal is the share-creation form. Opened from the context menu of a session
// or repository row it receives initialTarget, so the target is fixed and the picker is
// hidden; opened from "new share" in the share list (ShareListModal) it has no
// initialTarget and the target is chosen here. It follows the same as="form" +
// ui-field/ui-seg + ui-modal-foot shape as the other creation forms (NewRepoModal, …).
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useDebounced } from "../../lib/useDebounced.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { agentOf } from "../../agents/registry.ts";
import { useMySharesStore } from "./store.ts";
import { boundedRequest, type BoundedRequest } from "./boundedRequest.ts";
import "./sharing.css";

interface RecipientCandidate {
  userKey: string;
  email: string;
}

interface ShareCreateModalProps {
  /** "session:<name>" | "repo:<workingCopyId>" | "worktree:<workingCopyId>" — when set
   * (opened from a context menu), the target is fixed and the picker is hidden. */
  initialTarget?: string;
  onClose: () => void;
  onCreated?: () => void;
}

export function ShareCreateModal({ initialTarget, onClose, onCreated }: ShareCreateModalProps) {
  const tr = useT();
  const toast = useToast();
  const repos = useReposStore((s) => s.repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const [target, setTarget] = useState(initialTarget ?? "");
  const [recipientQuery, setRecipientQuery] = useState("");
  const [recipientPick, setRecipientPick] = useState<RecipientCandidate | null>(null);
  const [suggestions, setSuggestions] = useState<RecipientCandidate[]>([]);
  const [searching, setSearching] = useState(false);
  const [permission, setPermission] = useState<"ro" | "rw">("ro");
  const [saving, setSaving] = useState(false);
  const inflight = useRef<BoundedRequest | null>(null);
  const locked = !!initialTarget;
  const debouncedQuery = useDebounced(recipientQuery, 250);
  useEffect(() => {
    // Once a recipient is picked the suggestions are pointless (the field shows a chip).
    if (recipientPick) return;
    let alive = true;
    setSearching(true);
    api(`api/session-share-recipients?q=${encodeURIComponent(debouncedQuery)}`)
      .then((d) => { if (alive) setSuggestions(d?.error ? [] : d.members || []); })
      .catch(() => { if (alive) setSuggestions([]); })
      .finally(() => { if (alive) setSearching(false); });
    return () => { alive = false; };
  }, [debouncedQuery, recipientPick]);
  // Archived sessions are not offered: they drop out of the recipient's list too
  // (docs/log/59 §1), so sharing one would show the recipient nothing.
  const candidates = useMemo(() => [
    ...sessions.filter((s) => agentOf(s.kind).caps.transcript).map((s) => ({
      value: `session:${s.name}`, label: `${tr("share.session_scope")}: ${s.title || s.label || s.name}`,
    })),
    ...repos.filter((r) => r.workingCopyId).map((r) => ({
      value: `${r.worktree ? "worktree" : "repo"}:${r.workingCopyId}`,
      label: `${r.worktree ? tr("share.worktree_scope") : tr("share.repo_scope")}: ${r.name}`,
    })),
  ], [repos, sessions, tr]);
  useEffect(() => { if (!locked && !target && candidates[0]) setTarget(candidates[0].value); }, [locked, candidates, target]);
  const targetLabel = candidates.find((c) => c.value === target)?.label ?? target;
  const canSubmit = !!target && !!recipientPick;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || saving) return;
    const [type, key] = target.split(":", 2);
    if (!type || !key) return;
    setSaving(true);
    const req = boundedRequest();
    inflight.current = req;
    const d = await api("api/session-shares", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ recipientUserKey: recipientPick.userKey, scope: { type, key }, permission }),
      signal: req.signal,
    }).catch(() => (req.signal.aborted && !req.timedOut() ? null : { error: { message: tr(req.timedOut() ? "share.save_timeout" : "share.save_failed") } }));
    req.settle();
    // Closed or unmounted meanwhile, or aborted by the user: never report that as created.
    if (inflight.current !== req || d === null) return;
    inflight.current = null;
    setSaving(false);
    if (d?.error) {
      toast(errText(d.error));
      // A timed-out create may still land on the server; show whatever it did.
      if (req.timedOut()) void useMySharesStore.getState().refresh();
      return;
    }
    void useMySharesStore.getState().refresh();
    onCreated?.();
    onClose();
  };
  // Closing never waits for the request: it abandons it. The server may still finish it,
  // so the share list is refreshed rather than assumed unchanged.
  const close = () => {
    const req = inflight.current;
    if (req) {
      inflight.current = null;
      req.cancel();
      void useMySharesStore.getState().refresh();
    }
    onClose();
  };
  useEffect(() => () => {
    const req = inflight.current;
    inflight.current = null; // first, so the pending submit sees it was abandoned
    req?.cancel();
  }, []);

  return (
    <Modal title={tr("share.create_title")} onClose={close} as="form" onSubmit={submit} className="share-create-modal">
      <div className="ui-modal-body">
        <p className="ui-field-hint">{tr("share.exposure_warning")}</p>
        {/* Marks (docs/log/69 / ADR 0050 decision 6). Showing the author's name is required
            so that it is clear who drew a mark, but recipients learning each other's login
            id is a new exposure, so it is stated here rather than hidden. */}
        <p className="ui-field-hint">{tr("share.marks_warning")}</p>
        {locked ? (
          <div className="ui-field">
            <span className="ui-field-label">{tr("share.target")}</span>
            <div className="share-target-fixed"><Icon name="broadcast" /> {targetLabel}</div>
          </div>
        ) : (
          <label className="ui-field">
            <span className="ui-field-label">{tr("share.target")}</span>
            <select value={target} onChange={(e) => setTarget(e.target.value)}>
              {candidates.map((c) => <option value={c.value} key={c.value}>{c.label}</option>)}
            </select>
          </label>
        )}
        {/* Sharing a repo covers the whole project: the base and every worktree under it
            (docs/log/59 §1). State how far the exposure reaches before sharing. */}
        {target.startsWith("repo:") && <p className="ui-field-hint">{tr("share.repo_scope_hint")}</p>}
        <div className="ui-field share-recipient-field">
          <span className="ui-field-label">{tr("share.recipient")}</span>
          {recipientPick ? (
            <div className="share-target-fixed">
              <Icon name="account" /> {recipientPick.email}
              <Button small variant="ghost" onClick={() => { setRecipientPick(null); setRecipientQuery(""); }}>
                {tr("share.recipient_change")}
              </Button>
            </div>
          ) : (
            <>
              <input {...NO_AUTOFILL}
                type="search"
                value={recipientQuery}
                onChange={(e) => setRecipientQuery(e.target.value)}
                placeholder={tr("share.recipient_search_ph")}
                autoFocus={locked}
              />
              {(searching || suggestions.length > 0 || debouncedQuery) && (
                <ul className="share-recipient-suggest">
                  {searching && <li className="ui-field-hint">{tr("common.loading")}</li>}
                  {!searching && suggestions.length === 0 && <li className="ui-field-hint">{tr("share.recipient_no_match")}</li>}
                  {!searching && suggestions.map((c) => (
                    <li key={c.userKey}>
                      <button type="button" className="ui-menu-item" onClick={() => setRecipientPick(c)}>
                        <Icon name="account" /> {c.email}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
        <div className="ui-field">
          <span className="ui-field-label">{tr("share.permission")}</span>
          <div className="ui-seg">
            <button type="button" className={"seg-btn" + (permission === "ro" ? " seg-btn--active" : "")} onClick={() => setPermission("ro")}>{tr("share.permission_ro")}</button>
            <button type="button" className={"seg-btn" + (permission === "rw" ? " seg-btn--active" : "")} onClick={() => setPermission("rw")}>{tr("share.permission_rw")}</button>
          </div>
          {permission === "rw" && <span className="ui-field-hint">{tr("share.approval_required")}</span>}
        </div>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={close}>{tr("ui.cancel")}</Button>
        <Button variant="primary" type="submit" icon={saving ? "loading" : "broadcast"} className={saving ? "share-busy" : undefined}
          aria-busy={saving} disabled={!canSubmit || saving}>
          {saving ? tr("share.saving") : tr("share.create")}
        </Button>
      </footer>
    </Modal>
  );
}
