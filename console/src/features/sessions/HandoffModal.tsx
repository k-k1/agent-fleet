// HandoffModal — the unified "引き継ぎ" dialog. Replaces the per-target inline menu items
// that only showed for a few kinds. It opens for ANY session that has a conversation
// (caps.transcript), lets the user pick the target agent from the launchable+connected
// kinds, and (via actions.handoff) opens an operator chat that fires the extraction turn
// automatically — the assistant is called directly and comes back with a handoff proposal.
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { api } from "../../core/api/client.ts";
import { AGENTS, agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import type { Session, SessionKind, ConnectionsStatus } from "../../types/session.ts";
import type { SessionActions } from "./useSessionActions.tsx";

interface HandoffModalProps {
  session: Session;
  actions: SessionActions;
  onClose: () => void;
}

export function HandoffModal({ session, actions, onClose }: HandoffModalProps) {
  const tr = useT();
  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  const [connsDone, setConnsDone] = useState(false);
  const [note, setNote] = useState("");
  const [target, setTarget] = useState<SessionKind | "">("");
  const [busy, setBusy] = useState(false);

  // Which agents can receive the handoff: a launchable kind (repoLaunchKinds, so ssm is
  // out) that owns a conversation (caps.transcript, so shell is out) and is connected. The
  // same auth gate the repo launch pickers use — an unauthenticated agent would fail on
  // create_session, so don't offer it.
  const targets = useMemo(
    () => repoLaunchKinds.filter((k) => AGENTS[k].caps.transcript && !!conns && agentOf(k).available({ conns })),
    [conns],
  );

  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => {
        if (!alive) return;
        setConns(d && !d.error ? (d as ConnectionsStatus) : null);
        setConnsDone(true);
      })
      .catch(() => {
        if (!alive) return;
        setConnsDone(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  // Default the target once the list settles: prefer the source session's own kind (a
  // like-for-like handoff), else the first available.
  useEffect(() => {
    if (target || targets.length === 0) return;
    setTarget(targets.includes(session.kind) ? session.kind : targets[0]);
  }, [targets, target, session.kind]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!target || busy) return;
    setBusy(true);
    try {
      await actions.handoff(session.name, target, note);
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={tr("handoff.title", { name: displayName(session) })}
      onClose={onClose}
      className="session-modal"
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field-hint">{tr("handoff.intro")}</div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("handoff.target_label")}</div>
          {!connsDone ? (
            <div className="ui-field-hint">{tr("handoff.checking")}</div>
          ) : targets.length === 0 ? (
            <div className="ui-field-hint">{tr("handoff.no_targets")}</div>
          ) : (
            <div className="ui-seg handoff-targets">
              {targets.map((k) => (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn" + (target === k ? " active" : "")}
                  onClick={() => setTarget(k)}
                >
                  <Icon name={agentOf(k).icon} /> {agentOf(k).displayName ?? agentOf(k).label}
                </button>
              ))}
            </div>
          )}
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("handoff.note_label")}</div>
          <div className="ui-field-hint">{tr("handoff.note_hint")}</div>
          <label className="ui-field">
            <textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={3}
              placeholder={tr("handoff.note_ph")}
            />
          </label>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose}>
          {tr("common.cancel")}
        </button>
        <button type="submit" className="ui-btn ui-btn-primary" disabled={!target || busy}>
          {tr("handoff.start")}
        </button>
      </footer>
    </Modal>
  );
}
