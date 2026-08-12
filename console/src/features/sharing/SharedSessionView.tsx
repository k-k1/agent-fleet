import { useEffect, useRef, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useSharedSessionsStore } from "./store.ts";
import "./sharing.css";

interface SharedTurn {
  idx?: number;
  role?: string;
  text?: string;
  ts?: string;
  parts?: Array<{ kind?: string; text?: string; tool?: string; info?: string }>;
}
export function SharedSessionView({ sharedSessionId }: { sharedSessionId: string }) {
  const tr = useT();
  const meta = useSharedSessionsStore((s) => s.sessions.find((x) => x.id === sharedSessionId));
  const refreshList = useSharedSessionsStore((s) => s.refresh);
  const [turns, setTurns] = useState<SharedTurn[]>([]);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const cursor = useRef(0);

  useEffect(() => {
    cursor.current = 0;
    setTurns([]);
    let live = true;
    let timer = 0;
    const tick = async () => {
      await refreshList();
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (!live) return;
      if (!current || current.workspaceState !== "running") {
        setError(current ? tr("share.owner_stopped") : tr("share.no_access"));
        timer = window.setTimeout(tick, 5000);
        return;
      }
      const first = cursor.current === 0;
      const path = first
        ? `api/shared-sessions/${encodeURIComponent(sharedSessionId)}/messages?since=0&tail=1&limit=80`
        : `api/shared-sessions/${encodeURIComponent(sharedSessionId)}/messages?since=${cursor.current}`;
      const d = await api(path).catch(() => ({ error: { message: tr("share.load_failed") } }));
      if (!live) return;
      if (d?.error) setError(errText(d.error));
      else {
        setError("");
        if (typeof d.cursor === "number") cursor.current = d.cursor;
        const incoming = Array.isArray(d.messages) ? d.messages : [];
        if (d.reset) setTurns(incoming);
        else if (incoming.length) setTurns((old) => [...old, ...incoming]);
      }
      timer = window.setTimeout(tick, 2500);
    };
    void tick();
    return () => { live = false; window.clearTimeout(timer); };
  }, [sharedSessionId, refreshList, tr]);

  if (!meta) return <div className="shared-view-empty"><Icon name="lock" /> {tr("share.no_access")}</div>;

  const propose = async () => {
    const prompt = draft.trim();
    if (!prompt || sending) return;
    setSending(true);
    const d = await apiJSON(`api/shared-sessions/${encodeURIComponent(meta.id)}/proposals`, "POST", {
      action: "turn",
      payload: { op: "start", prompt },
    }).catch(() => ({ error: { message: tr("share.proposal_failed") } }));
    setSending(false);
    if (d?.error) setError(errText(d.error));
    else { setDraft(""); setError(tr("share.proposal_sent")); }
  };

  return (
    <div className="shared-view">
      <header className="shared-view-head">
        <div><Icon name="broadcast" /> <strong>{meta.title || meta.label || meta.name}</strong></div>
        <small>{meta.ownerUserKey} · {meta.permission.toUpperCase()} · {meta.archived ? tr("share.archived") : meta.state}</small>
      </header>
      <div className="shared-view-body" tabIndex={-1}>
        {error && <div className="shared-view-notice">{error}</div>}
        {turns.map((turn, i) => (
          <article className={`shared-turn ${turn.role === "user" ? "user" : "assistant"}`} key={`${turn.idx ?? i}-${i}`}>
            <div className="shared-turn-role">{turn.role === "user" ? tr("share.user") : tr("share.assistant")}</div>
            {turn.text && <MarkdownView source={turn.text} onOpenSession={() => {}} onOpenConversation={() => {}} />}
            {turn.parts?.map((part, j) => part.kind === "text" && part.text
              ? <MarkdownView key={j} source={part.text} onOpenSession={() => {}} onOpenConversation={() => {}} />
              : part.kind === "tool" ? <div className="shared-tool" key={j}><Icon name="tools" /> {part.tool}{part.info ? ` · ${part.info}` : ""}</div> : null)}
          </article>
        ))}
      </div>
      {meta.permission === "rw" && meta.workspaceState === "running" && (
        <div className="shared-propose">
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={tr("share.proposal_placeholder")} />
          <Button variant="primary" icon="send" disabled={!draft.trim() || sending} onClick={() => void propose()}>
            {tr("share.propose")}
          </Button>
          <small>{tr("share.owner_approval_note")}</small>
        </div>
      )}
    </div>
  );
}
