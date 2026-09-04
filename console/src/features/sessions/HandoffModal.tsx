// HandoffModal — the unified "handoff" dialog. Replaces the per-target inline menu items
// that only showed for a few kinds. It opens for ANY session that has a conversation
// (caps.transcript), lets the user pick the target agent, and (via actions.handoff) opens
// an operator chat that fires the extraction turn automatically — the assistant is called
// directly and comes back with a handoff proposal.
//
// The target picker is deliberately identical to the repo launch modal (same connected +
// runsInDir agent set, same order, same `ui-seg big` seg-buttons). It reads the shared
// connections cache the always-mounted repo rail warms, so the list appears instantly
// instead of a beat late; it only self-fetches when the cache is still cold.
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type { FormEvent, KeyboardEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { api } from "../../core/api/client.ts";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { getCachedConns, setCachedConns, subscribeConns } from "../repos/connsCache.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSettings } from "../../lib/settings.ts";
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
  const settings = useSettings();
  const conns = useSyncExternalStore(subscribeConns, getCachedConns, getCachedConns);
  const [tried, setTried] = useState(false); // a cold-cache self-fetch has settled
  const [note, setNote] = useState("");
  const [target, setTarget] = useState<SessionKind | "">("");
  const [busy, setBusy] = useState(false);

  // Same agent set the launch modal offers: a connected, in-a-dir agent (runsInDir drops
  // shell/ssm), in repoLaunchKinds order. Every such kind also owns a conversation, so it
  // can be a handoff target.
  const targets = useMemo(
    () => repoLaunchKinds.filter((k) => agentOf(k).caps.runsInDir && !!conns && agentOf(k).available({ conns })),
    [conns],
  );

  // Only fetch when the rail hasn't already warmed the cache — the common case is a hit,
  // so the list renders on the first frame.
  useEffect(() => {
    if (conns) return;
    let alive = true;
    api("api/connections")
      .then((d) => {
        if (alive) setCachedConns(d && !d.error ? (d as ConnectionsStatus) : null);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setTried(true);
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- run once on open; `conns` is the warm-cache guard
  }, []);
  const settling = !conns && !tried;

  // Default the target once the list settles: prefer the source session's own kind (a
  // like-for-like handoff), else the first available.
  useEffect(() => {
    if (target || targets.length === 0) return;
    setTarget(targets.includes(session.kind) ? session.kind : targets[0]);
  }, [targets, target, session.kind]);

  const start = async () => {
    if (!target || busy) return;
    setBusy(true);
    try {
      await actions.handoff(session.name, target, note);
      onClose();
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void start();
  };
  const onNoteKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.metaKey || e.ctrlKey;
    const submitWithKey = settings.mirrorSend !== "enter" ? mod : !e.shiftKey && !mod;
    if (submitWithKey) {
      e.preventDefault();
      void start();
    }
  };

  return (
    <Modal
      title={tr("handoff.title", { name: displayName(session) })}
      onClose={onClose}
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field-hint">{tr("handoff.intro")}</div>

        <div className="ui-field">
          <span className="ui-field-label">{tr("handoff.target_label")}</span>
          {targets.length === 0 && (
            // Empty means one of two things — still checking, or nothing is connected.
            // Same copy as the launch modal so the two pickers read identically.
            <div className="muted launch-noagents">
              {tr(settling ? "launch.agents_checking" : "launch.agents_none")}
            </div>
          )}
          <div className="ui-seg big">
            {targets.map((k) => {
              const a = agentOf(k);
              return (
                <button
                  key={k}
                  type="button"
                  title={tr(a.launchHintKey)}
                  className={"seg-btn kind-" + a.cssClass + (target === k ? " active" : "")}
                  onClick={() => setTarget(k)}
                >
                  <Icon name={a.icon} className="seg-ic" />
                  {kindDisplayName(k)}
                </button>
              );
            })}
          </div>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("handoff.note_label")}</div>
          <div className="ui-field-hint">{tr("handoff.note_hint")}</div>
          <label className="ui-field">
            <textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={onNoteKeyDown}
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
