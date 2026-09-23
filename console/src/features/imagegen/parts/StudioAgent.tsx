// StudioAgent — the studio's left column (ADR 0100 §4): the bound session's mirror, embedded
// as it is (thinking, tool cards, attachments, stop and resume come with it), under a head that
// names the agent and offers to switch it. Without a session, the way to attach one.
import { useEffect, useState } from "react";
import { apiJSON, errText } from "../../../core/api/client.ts";
import { agentOf } from "../../../agents/registry.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { isManagedSession } from "../../../types/session.ts";
import { MirrorView, type MirrorSignal } from "../../mirror/MirrorView.tsx";
import { openSessionTerminalSplit } from "../../sessions/open.ts";
import { useSessionsStore } from "../../sessions/store.ts";
import { studioPersona } from "../api.ts";

export function StudioAgent({
  paneId,
  studioId,
  session,
  active,
  signal,
  onAttach,
  onReplace,
}: {
  paneId: string;
  studioId: string | null;
  session: string;
  active: boolean;
  signal?: MirrorSignal;
  onAttach: () => void;
  onReplace: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const meta = useSessionsStore((s) => (session ? s.sessions.find((x) => x.name === session) ?? null : null));
  // The list has arrived at least once; before that a missing row is not "gone".
  const loaded = useSessionsStore((s) => s.sessions.length > 0);
  const startSession = useSessionsStore((s) => s.start);
  const [attached, setAttached] = useState(meta?.alive === true);
  const [resending, setResending] = useState(false);
  useEffect(() => {
    if (meta?.alive === true) setAttached(true);
    else if (meta?.alive === false) setAttached(false);
  }, [meta?.alive]);

  if (!session) {
    return (
      <div className="igen-agent igen-agent-none">
        <EmptyState icon="hubot" title={tr("imggen.agent_none")} hint={tr("imggen.agent_none_hint")}>
          <Button variant="primary" icon="add" onClick={onAttach}>
            {tr("imggen.attach")}
          </Button>
        </EmptyState>
      </div>
    );
  }
  if (!meta) {
    return (
      <div className="igen-agent igen-agent-none">
        <EmptyState icon={loaded ? "warning" : "loading"} title={tr(loaded ? "imggen.agent_gone" : "imggen.agent_loading")}>
          {loaded && (
            <Button variant="primary" icon="add" onClick={onAttach}>
              {tr("imggen.attach")}
            </Button>
          )}
        </EmptyState>
      </div>
    );
  }

  const managed = isManagedSession(meta);
  const state = meta.initialPromptState;
  // decision 2: while the delivery is still running, no resend — the original goroutine may yet
  // send, and a resend then makes the persona arrive twice.
  const resend = async () => {
    if (!studioId) return;
    setResending(true);
    try {
      const p = await studioPersona(studioId);
      if (!p || p.error || !p.prompt) {
        toast((p?.error && errText(p.error)) || tr("imggen.persona_failed"), { kind: "error" });
        return;
      }
      const r = await apiJSON(`api/sessions/${encodeURIComponent(session)}/input`, "POST", { prompt: p.prompt, when_ready: true });
      if (r?.error) toast(errText(r.error) || tr("imggen.persona_failed"), { kind: "error" });
      else toast(tr("imggen.persona_resent"), { kind: "info" });
    } catch {
      toast(tr("imggen.persona_failed"), { kind: "error" });
    } finally {
      setResending(false);
    }
  };

  return (
    <div className="igen-agent">
      <div className="igen-agent-head">
        <span className={"igen-chip kind-" + agentOf(meta.kind).cssClass}>
          <Icon name={agentOf(meta.kind).icon} /> {kindDisplayName(meta.kind)}
        </span>
        {meta.model && <span className="igen-chip">{meta.model}</span>}
        <span className="igen-chip">{tr(managed ? "imggen.attach_driver_managed" : "imggen.attach_driver_tui")}</span>
        <span className="igen-agent-spacer" />
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={onReplace}>
          <Icon name="arrow-swap" /> {tr("imggen.agent_replace")}
        </button>
      </div>
      {state === "pending" && (
        <div className="igen-persona pending">
          <Icon name="loading" spin /> {tr("imggen.persona_pending")}
        </div>
      )}
      {(state === "failed" || state === "unknown") && (
        <div className="igen-persona failed">
          <Icon name="warning" /> {tr(state === "failed" ? "imggen.persona_state_failed" : "imggen.persona_state_unknown")}
          <button type="button" className="ui-btn ui-btn-sm" disabled={resending} onClick={() => void resend()}>
            {tr("imggen.persona_resend")}
          </button>
        </div>
      )}
      <div className="igen-agent-mirror">
        <MirrorView
          paneId={paneId}
          session={session}
          sessionMeta={meta}
          active={active}
          mirror
          // The studio has no terminal of its own; the terminal toggle opens one beside it.
          onToggleMirror={(toChat) => {
            if (!toChat && !managed) openSessionTerminalSplit(session);
          }}
          readOnly={!attached}
          onResume={() => {
            void (async () => {
              if (meta.alive !== true && !(await startSession(session))) return;
              setAttached(true);
            })();
          }}
          signal={signal}
        />
      </div>
    </div>
  );
}
