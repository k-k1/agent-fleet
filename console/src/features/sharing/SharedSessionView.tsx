import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { expandThinking, useSettings } from "../../lib/settings.ts";
import { TranscriptView } from "../mirror/transcript/TranscriptView.tsx";
import type { TranscriptCaps } from "../mirror/transcript/capabilities.ts";
import type { Turn } from "../mirror/transcript/types.ts";
import { coalesceUserActions, groupTurns } from "../mirror/transcript/model.ts";
import { useSharedSessionsStore } from "./store.ts";
import "./sharing.css";

// SharedSessionView — the RECIPIENT's read of a session somebody else owns (docs/59).
//
// It renders through the very same pipeline and blocks as the mirror
// (features/mirror/transcript), so a shared conversation reads exactly like the owner's:
// tool runs folded, plans and reasoning as their own cards, compaction summaries
// collapsed. What differs is only the TranscriptCaps handed in — a recipient has no
// Workspace of their own to open files, diffs, panes or pasted images in, so those
// affordances are simply not rendered (see transcript/capabilities.ts).
//
// The transcript arrives through the control-plane's allowlist DTO, which strips cwd /
// path / filePath and every structured coordinate before it ever reaches the browser
// (docs/59 §3, control-plane/session_share.go sharedTranscriptDTO).

// First page size. Smaller than the mirror's 400: the first screenful should paint fast,
// and everything earlier is one scroll away (`before=` paging, below).
const WINDOW = 60;
// Poll cadence, matching the mirror's. The server allows 120 reads/min per
// recipient+session, so even the working cadence stays well inside the limit.
const POLL_WORKING = 1200;
const POLL_IDLE = 3000;
// The owner's Workspace is stopped: nothing can change until they start it, so back off.
const POLL_STOPPED = 5000;
const NEAR_BOTTOM_PX = 80;

interface SharedTurn extends Turn {
  status?: string;
}

// Last-known transcript per shared session, kept at module level so re-opening a pane
// paints immediately instead of starting from an empty view while the first fetch flies.
// Same reasoning as the mirror's echoStore: this component unmounts on a pane/tab switch,
// and re-fetching from scratch every time is exactly what made the view feel slow.
interface CacheEntry {
  turns: SharedTurn[];
  cursor: number;
  firstLine: number;
  hasMore: boolean;
}
const transcriptCache = new Map<string, CacheEntry>();

export function SharedSessionView({ sharedSessionId, headerActions }: { sharedSessionId: string; headerActions?: ReactNode }) {
  const tr = useT();
  const settings = useSettings();
  const meta = useSharedSessionsStore((s) => s.sessions.find((x) => x.id === sharedSessionId));
  const refreshList = useSharedSessionsStore((s) => s.refresh);
  const cached = transcriptCache.get(sharedSessionId);
  const [turns, setTurns] = useState<SharedTurn[]>(cached?.turns ?? []);
  const [loaded, setLoaded] = useState(!!cached);
  const [hasMore, setHasMore] = useState(cached?.hasMore ?? false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const cursor = useRef(cached?.cursor ?? 0);
  const firstLine = useRef(cached?.firstLine ?? 0);
  const bodyRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  // Set while prepending older history, to keep the reader's position put (below).
  const anchor = useRef<number | null>(null);

  const path = `api/shared-sessions/${encodeURIComponent(sharedSessionId)}`;

  useEffect(() => {
    const entry = transcriptCache.get(sharedSessionId);
    setTurns(entry?.turns ?? []);
    setLoaded(!!entry);
    setHasMore(entry?.hasMore ?? false);
    setError("");
    cursor.current = entry?.cursor ?? 0;
    firstLine.current = entry?.firstLine ?? 0;
    atBottom.current = true;
    // Kick a list refresh so the header meta fills in if the store is still cold — but
    // never await it. Blocking the first transcript fetch behind a full
    // GET /api/shared-sessions (which probes every owner's Workspace state in turn) was
    // the single biggest reason a shared session took so long to show anything.
    void refreshList();

    let live = true;
    let timer = 0;
    const tick = async () => {
      if (!live) return;
      // Read the owner's Workspace state from the store rather than fetching it: the
      // global 5s poll (store.ts startSharedSessionsPolling) already keeps it current.
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (current && current.workspaceState !== "running") {
        setError(tr("share.owner_stopped"));
        timer = window.setTimeout(tick, POLL_STOPPED);
        return;
      }
      // A missing entry is NOT treated as "no access": the store may simply not have
      // loaded yet, and the server is the authority — it answers 404 if the share is gone.
      const first = cursor.current === 0;
      const url = first ? `${path}/messages?since=0&tail=1&limit=${WINDOW}` : `${path}/messages?since=${cursor.current}`;
      const d = await api(url).catch(() => ({ error: { message: tr("share.load_failed") } }));
      if (!live) return;
      if (d?.error) {
        setError(errText(d.error));
      } else {
        setError("");
        setLoaded(true);
        if (typeof d.cursor === "number") cursor.current = d.cursor;
        if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
        if (typeof d.hasMore === "boolean") setHasMore(d.hasMore);
        setWorking(d.status === "working");
        const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
        if (d.reset) setTurns(incoming);
        else if (incoming.length) setTurns((old) => [...old, ...incoming]);
      }
      timer = window.setTimeout(tick, d?.status === "working" ? POLL_WORKING : POLL_IDLE);
    };
    void tick();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [sharedSessionId, refreshList, tr, path]);

  // Keep the module cache in step so the next mount paints from it.
  useEffect(() => {
    if (loaded) {
      transcriptCache.set(sharedSessionId, { turns, cursor: cursor.current, firstLine: firstLine.current, hasMore });
    }
  }, [sharedSessionId, turns, hasMore, loaded]);

  // Older history, one page at a time. The server already supports `before=` (it proxies
  // the query through to the Agent) and the DTO already passes firstLine/hasMore — this
  // just uses them, which is what lets the first fetch stay small.
  const loadOlder = async () => {
    const el = bodyRef.current;
    if (!el || loadingOlder || firstLine.current <= 0) return;
    setLoadingOlder(true);
    const keep = el.scrollHeight - el.scrollTop; // distance from the bottom, held across the prepend
    const d = await api(`${path}/messages?before=${firstLine.current}&limit=${WINDOW}`).catch(() => null);
    if (d && !d.error) {
      const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
      if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
      setHasMore(!!d.hasMore);
      if (incoming.length) {
        anchor.current = keep;
        setTurns((old) => [...incoming, ...old]);
      }
    }
    setLoadingOlder(false);
  };

  // Restore the reading position after a prepend, and otherwise follow the tail.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      anchor.current = null;
      return;
    }
    if (atBottom.current) el.scrollTop = el.scrollHeight;
  }, [turns]);

  const onScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX;
  };

  const propose = async () => {
    const prompt = draft.trim();
    if (!prompt || sending || !meta) return;
    setSending(true);
    const d = await apiJSON(`api/shared-sessions/${encodeURIComponent(meta.id)}/proposals`, "POST", {
      action: "turn",
      payload: { op: "start", prompt },
    }).catch(() => ({ error: { message: tr("share.proposal_failed") } }));
    setSending(false);
    if (d?.error) setError(errText(d.error));
    else {
      setDraft("");
      setError(tr("share.proposal_sent"));
    }
  };

  const groups = groupTurns(coalesceUserActions(turns));

  // A recipient can read, and nothing else. Every capability the mirror fills in is
  // deliberately absent here — there is no local file to open, no diff pane, no pasted
  // image to fetch from someone else's Workspace, no fork, and no agent of theirs to
  // re-authenticate. The blocks drop those affordances instead of showing dead controls,
  // and fall back to self-contained renderings (tool edits and plans expand in place).
  const caps: TranscriptCaps = {
    agentName: agentOf(meta?.kind).assistantName,
    expandThinking: expandThinking(settings, meta?.kind),
  };

  return (
    <div className="shared-view">
      <header className="shared-view-head">
        <div className="shared-view-info">
          <div>
            <Icon name="broadcast" /> <strong>{meta?.title || meta?.label || meta?.name || tr("share.shared_sessions")}</strong>
          </div>
          {meta && (
            <small>
              {meta.ownerUserKey} · {tr(meta.permission === "rw" ? "share.permission_rw" : "share.permission_ro")} ·{" "}
              {meta.state}
            </small>
          )}
        </div>
        {headerActions && <span className="view-head-actions">{headerActions}</span>}
      </header>
      <div className="shared-view-body" ref={bodyRef} onScroll={onScroll} tabIndex={-1}>
        <div className="mirror-scroll">
          {error && <div className="shared-view-notice">{error}</div>}
          {loaded && hasMore && (
            <div className="mirror-loadmore">
              <button type="button" className="ghost mirror-loadmore-btn" disabled={loadingOlder} onClick={() => void loadOlder()}>
                {loadingOlder ? (
                  <>
                    <Icon name="loading" spin /> {tr("chat.ph_loading")}
                  </>
                ) : (
                  <>
                    <Icon name="chevron-up" /> {tr("mirror.load_earlier")}
                  </>
                )}
              </button>
            </div>
          )}
          {!loaded ? (
            !error && (
              <div className="mirror-empty muted mirror-loading">
                <Icon name="loading" spin /> {tr("chat.ph_loading")}
              </div>
            )
          ) : groups.length === 0 ? (
            <div className="mirror-empty muted">{tr("mirror.no_history")}</div>
          ) : (
            <TranscriptView groups={groups} caps={caps} working={working} autoCollapseWork={atBottom.current} />
          )}
        </div>
      </div>
      {meta?.permission === "rw" && meta.workspaceState === "running" && (
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
