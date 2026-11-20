// MemoryEntryView — one memory in a pane of its own, read and edited like a file (ADR 0141
// decision 4). The body renders as Markdown; Edit puts description and type in a form above the
// code editor, and the body in the editor. A save goes through member-save, which runs the agents'
// memory_save checks (revision, secret scan, limits) and records the member as author; the CP signs
// it, as it signs pin, forget and unforget (ADR 0113). Nothing here writes a file directly.
//
// A forgotten memory has no body: it opens read-only from its last change (member-latest), whose
// revert brings it back. A memory whose text fails the secret scan is never readable here (the
// Agent refuses the read), so no body of one reaches this DOM.
//
// The draft lives outside React (drafts below), keyed by the memory: a tabbed cell renders only its
// selected view, so a tab switch unmounts this one, and held in state the text would go with it.
// Close, reload and logout guards for it are #4569.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail, isTransientErr } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useT } from "../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../lib/intl.ts";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { CodeEditor } from "../editor/CodeEditor.tsx";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { validationErrText } from "../viewer/parts/fileStatusText.ts";
import { MemoryHistory, useAllChanges } from "../settings/memory/memoryHistory.tsx";
import { PurgeHistoryDialog, type PurgeTarget } from "../settings/memory/memoryPurge.tsx";
import type { MemoryBody, MemoryChange, SecretFinding } from "../settings/memory/memoryTypes.ts";
import type { PaneContent } from "../../layout/types.ts";
import { memoryEntryContent } from "./openEntry.ts";
import { openMemoryView } from "./open.ts";
import { useMemoryStore } from "./store.ts";

type EntryContent = Extract<PaneContent, { kind: "memoryEntry" }>;

const TYPES = ["user", "feedback", "project", "reference"] as const;
const projectQuery = (project: string) => (project ? "?project=" + encodeURIComponent(project) : "");

export interface Draft {
  /** The revision the edit started from; 0 for a new memory. A save never lands on a newer one. */
  revision: number;
  /** Kept as read: a save replaces the kinds, and an edit of the body must not widen who sees it. */
  kinds: string[];
  name: string;
  description: string;
  type: string;
  body: string;
}

// The drafts of every memory pane, by memory. Never persisted: a page reload starts clean.
const drafts = new Map<string, Draft>();
export const draftKey = (c: { scope: string; project: string; name: string }) => `${c.scope}:${c.project}/${c.name}`;
/** Forget every draft; tests call it between cases. */
export function resetMemoryDrafts() {
  drafts.clear();
}

interface MemoryEntryViewProps {
  paneId: string;
  content: EntryContent;
  headerActions?: React.ReactNode;
}

export function MemoryEntryView({ paneId, content, headerActions }: MemoryEntryViewProps) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const version = useMemoryStore((s) => s.version);
  const bump = useMemoryStore((s) => s.bump);
  const { scope, project, name, gone } = content;
  const isNew = name === "";
  const key = draftKey(content);
  const where = scope === "user" ? tr("mem.af_scope_user") : content.projectName || project;

  const [body, setBody] = useState<MemoryBody | null>(null);
  const [change, setChange] = useState<MemoryChange | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [draft, setDraftState] = useState<Draft | null>(() => drafts.get(key) ?? (isNew ? blankDraft() : null));
  const [formErr, setFormErr] = useState("");
  const [findings, setFindings] = useState<SecretFinding[]>([]);
  const [busy, setBusy] = useState(false);
  const [purging, setPurging] = useState<PurgeTarget | null>(null);
  const [showHistory, setShowHistory] = useState(false);
  // setBusy is asynchronous state; the ref stops a second click that lands before it renders.
  const inFlight = useRef(false);

  const setDraft = useCallback(
    (d: Draft | null) => {
      if (d) drafts.set(key, d);
      else drafts.delete(key);
      setDraftState(d);
    },
    [key],
  );
  // The pane can be retargeted to another memory while mounted (a save of a new one, a forget).
  useEffect(() => {
    setDraftState(drafts.get(key) ?? (isNew ? blankDraft() : null));
    setFormErr("");
    setFindings([]);
  }, [key, isNew]);

  const running = wsState === "running";
  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running || isNew) return true;
      const q = `scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`;
      const res = await api(`api/agents/memory/entries/${gone ? "member-latest" : "member-read"}?${q}`);
      if (signal.aborted) return true;
      if (isTransientErr(res)) return false;
      if (res?.error) {
        setLoadErr(errDetail(res.error));
        setBody(null);
        setChange(null);
      } else if (gone) {
        setLoadErr("");
        setChange(res as MemoryChange);
      } else {
        setLoadErr("");
        setBody(res as MemoryBody);
      }
      return true;
    },
    [running, isNew, scope, project, name, gone],
  );
  useRetryLoad(load, [load, version]);
  const history = useAllChanges(version);

  const retarget = (next: Partial<EntryContent>) => setPaneTarget(paneId, { content: { ...content, ...next } });

  // A write the Agent refused leaves the pane as it was; any other outcome is re-read everywhere.
  const write = async (run: () => Promise<{ error?: { code?: string; message?: string } } | null>, ok: () => void) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      const res = await run();
      if (res?.error) toast(errDetail(res.error));
      else ok();
    } catch {
      toast(tr("mem.af_failed"));
    } finally {
      inFlight.current = false;
      setBusy(false);
      bump();
    }
  };

  const togglePin = () => {
    if (!body) return;
    const pinned = !body.pinned;
    // The project id also goes in the query: the CP audit ledger reads the URL only.
    void write(
      () => apiJSON("api/agents/memory/entries/pin" + projectQuery(project), "POST", { scope, project, name, pinned }),
      () => toast(tr(pinned ? "mem.pin_done" : "mem.pin_undone", { name }), { kind: "success" }),
    );
  };

  const forget = async () => {
    if (!body || inFlight.current) return;
    // Forget is the change list's own revert route, on the newest change of the memory.
    const latest = await api(`api/agents/memory/entries/member-latest?scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`).catch(
      () => null,
    );
    if (!latest || latest.error) return toast(latest?.error ? errDetail(latest.error) : tr("mem.af_failed"));
    if (!latest.commit || !latest.live || !latest.revertible) return toast(tr("mem.br_forget_unavailable", { name }));
    const ok = await askConfirm({
      title: tr("mem.af_forget_title"),
      body: <p>{tr("mem.af_forget_body", { name, where })}</p>,
      confirmLabel: tr("mem.af_forget"),
      danger: true,
    });
    if (!ok) return;
    await write(
      () =>
        apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(latest.commit), "POST", {
          commit: latest.commit,
          forget: true,
          ack: false,
          // The read and the change are apart: pin the forget to the revision this pane showed.
          expectRevision: body.revision,
        }),
      () => {
        toast(tr("mem.af_forgotten", { name }), { kind: "success" });
        setDraft(null);
        retarget({ gone: true });
      },
    );
  };

  const unforget = () => {
    if (!change) return;
    void write(
      async () => {
        const res = await apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(change.commit), "POST", {
          commit: change.commit,
          forget: false,
          ack: false,
        });
        return res?.error?.code === "memory_secret_detected" ? { error: { message: tr("mem.br_unforget_secret") } } : res;
      },
      () => {
        toast(tr("mem.br_unforgotten", { name }), { kind: "success" });
        setPaneTarget(paneId, { content: memoryEntryContent({ scope, project: { id: project, display: content.projectName }, name }) });
      },
    );
  };

  const startEdit = () => {
    if (!body) return;
    setFormErr("");
    setFindings([]);
    setDraft({
      revision: body.revision,
      kinds: body.kinds ?? [],
      name,
      description: body.description,
      type: body.type || "feedback",
      body: body.body,
    });
  };

  const save = async () => {
    if (!draft || inFlight.current) return;
    const saveName = draft.name.trim();
    inFlight.current = true;
    setBusy(true);
    setFindings([]);
    setFormErr("");
    try {
      // The project id also goes in the query: the CP audit ledger reads the URL only.
      const res = await apiJSON("api/agents/memory/entries/member-save" + projectQuery(project), "POST", {
        scope,
        project,
        name: saveName,
        description: draft.description,
        type: draft.type,
        kinds: draft.kinds,
        body: draft.body,
        revision: draft.revision,
      });
      if (res?.error?.code === "memory_secret_detected") {
        setFindings(res.findings ?? []);
        setFormErr(tr("mem.br_secret"));
      } else if (res?.error) {
        setFormErr(errDetail(res.error));
      } else {
        toast(tr("mem.br_saved", { name: saveName }), { kind: "success" });
        setDraft(null);
        bump();
        // A new memory becomes the pane of the memory it now is.
        if (isNew) setPaneTarget(paneId, { content: memoryEntryContent({ scope, project: { id: project, display: content.projectName }, name: saveName }) });
      }
    } catch {
      setFormErr(tr("mem.af_failed"));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  const cancel = () => {
    setDraft(null);
    setFormErr("");
    setFindings([]);
    // A new memory that is not saved has nothing to show: the pane goes back to the view.
    if (isNew) useLeftRail.getState().showView("memory");
  };

  const who = (m: { authorKind: string; authorSession: string }) =>
    m.authorKind === "member" ? tr("mem.af_by_member") : `${m.authorKind} · ${m.authorSession}`;

  const title = isNew ? (draft?.name.trim() || tr("pane.kind.memory_new")) : name;

  const actions = (
    <>
      {draft ? (
        <>
          <Button icon="save" disabled={busy} onClick={() => void save()}>
            {tr("mem.br_save")}
          </Button>
          <Button variant="ghost" disabled={busy} onClick={cancel}>
            {tr("mem.br_cancel")}
          </Button>
        </>
      ) : gone ? (
        change && (
          <>
            <Button icon="discard" disabled={busy} onClick={unforget}>
              {tr("mem.br_unforget")}
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => setPurging({ scope, project: { id: project }, name, where })}>
              {tr("mem.pg_button")}
            </Button>
          </>
        )
      ) : (
        body && (
          <>
            <Button icon="edit" disabled={busy} onClick={startEdit}>
              {tr("mem.br_edit")}
            </Button>
            <Button variant="ghost" icon={body.pinned ? "pinned" : "pin"} aria-pressed={body.pinned} disabled={busy} onClick={togglePin}>
              {tr(body.pinned ? "mem.pin_unpin" : "mem.pin_pin")}
            </Button>
            <Button variant="ghost" icon="trash" disabled={busy} onClick={() => void forget()}>
              {tr("mem.af_forget")}
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => setPurging({ scope, project: { id: project }, name, where })}>
              {tr("mem.pg_button")}
            </Button>
          </>
        )
      )}
      {headerActions}
    </>
  );

  return (
    <div className="mentry" data-memory-key={key}>
      <ViewHead actions={actions}>
        <span className="view-title">
          <Icon name="archive" /> {title}
        </span>
        <span className="mentry-where" title={where}>
          {where}
        </span>
        {gone && <span className="mem-badge">{tr("mem.md_f_forgotten")}</span>}
        {body?.pinned && !draft && <span className="mem-badge">{tr("mem.br_pinned")}</span>}
      </ViewHead>
      <div className="mentry-body">
        {!running ? (
          <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
            </Button>
          </EmptyState>
        ) : draft ? (
          <form
            className="mentry-form"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <div className="mentry-fields">
              {isNew && (
                <label>
                  {tr("mem.br_f_name")}
                  <input value={draft.name} autoFocus onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                </label>
              )}
              <label className="mentry-desc">
                {tr("mem.br_f_description")}
                <input value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
              </label>
              <label>
                {tr("mem.br_f_type")}
                <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}>
                  {TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {formErr && (
              <p className="mem-warn" role="alert">
                {formErr}
              </p>
            )}
            {findings.length > 0 && (
              <ul className="mem-findings">
                {findings.map((f, i) => (
                  <li key={i}>
                    <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                  </li>
                ))}
              </ul>
            )}
            <div className="mentry-editor" aria-label={tr("mem.br_f_body")}>
              <CodeEditor
                path={(draft.name || "memory") + ".md"}
                content={draft.body}
                wrap
                onChange={(text) => setDraft({ ...draft, body: text })}
                onSave={() => void save()}
                onValidationError={(err) => setFormErr(validationErrText(tr, err.code))}
              />
            </div>
          </form>
        ) : loadErr ? (
          <p className="mem-warn pad">{loadErr}</p>
        ) : gone ? (
          change ? (
            <div className="mentry-read">
              <p className="muted">{tr("mem.br_forgotten_note")}</p>
              <dl className="mentry-meta">
                <dt>{tr("mem.md_m_forgotten")}</dt>
                <dd>{fmtDateTime(change.at, DATETIME_FULL)}</dd>
                <dt>{tr("mem.md_m_author")}</dt>
                <dd>{who(change)}</dd>
              </dl>
            </div>
          ) : (
            <p className="muted pad">{tr("common.loading")}</p>
          )
        ) : body ? (
          <div className="mentry-read">
            <dl className="mentry-meta">
              {body.type && (
                <>
                  <dt>{tr("mem.br_f_type")}</dt>
                  <dd>{body.type}</dd>
                </>
              )}
              <dt>{tr("mem.md_m_author")}</dt>
              <dd>{who(body)}</dd>
              <dt>{tr("mem.md_m_updated")}</dt>
              <dd>{fmtDateTime(body.updated, DATETIME_FULL)}</dd>
              <dt>{tr("mem.md_m_uses")}</dt>
              <dd>{body.uses ?? 0}</dd>
            </dl>
            {body.description && <p className="mentry-desc-read">{body.description}</p>}
            <div className="mentry-md">
              <MarkdownView source={body.body} />
            </div>
          </div>
        ) : (
          <p className="muted pad">{tr("common.loading")}</p>
        )}
        {running && !isNew && !draft && (
          <div className="mentry-history">
            <button type="button" className="linklike" aria-expanded={showHistory} onClick={() => setShowHistory((v) => !v)}>
              <Icon name={showHistory ? "chevron-down" : "chevron-right"} /> {tr("mem.rv_history")}
            </button>
            {showHistory && (
              <MemoryHistory
                target={{ scope, project, name }}
                data={history}
                onOpenChanges={() => openMemoryView("changes")}
              />
            )}
          </div>
        )}
      </div>
      {purging && <PurgeHistoryDialog target={purging} onClose={() => setPurging(null)} onDone={bump} />}
    </div>
  );
}

function blankDraft(): Draft {
  return { revision: 0, kinds: [], name: "", description: "", type: "feedback", body: "" };
}
