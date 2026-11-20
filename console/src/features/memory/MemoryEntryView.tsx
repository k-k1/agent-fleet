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
// An existing memory is edited through useFileEditor with the adapter in memoryEntryIO: its buffer
// outlives a tab switch, the close / reload / logout guards cover it, and a save that lost a race
// opens the same resolution panel a file does. A new memory has no revision to guard and no name
// to key a buffer by yet, so its draft lives outside React (drafts below), keyed by the memory: a
// tabbed cell renders only its selected view, so a tab switch unmounts this one, and held in state
// the text would go with it.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, apiJSON, errDetail, isTransientErr } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
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
import { heldEditorBuffer, useFileEditor } from "../editor/useFileEditor.ts";
import { dirtyPaneIds } from "../editor/dirtyRegistry.ts";
import { EditorResolutionPanel } from "../viewer/parts/EditorResolutionPanel.tsx";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { isEditorAlert, validationErrText } from "../viewer/parts/fileStatusText.ts";
import { MemoryHistory, useAllChanges } from "../settings/memory/memoryHistory.tsx";
import { PurgeHistoryDialog, type PurgeTarget } from "../settings/memory/memoryPurge.tsx";
import type { MemoryBody, MemoryChange, SecretFinding } from "../settings/memory/memoryTypes.ts";
import type { PaneContent } from "../../layout/types.ts";
import { memoryEntryContent } from "./openEntry.ts";
import { openMemoryView } from "./open.ts";
import { useMemoryStore } from "./store.ts";
import { clearPending, clearRefusal, decodeEntry, encodeEntry, entryFile, isMemoryBody, memoryEntryIO, refusalOf, subscribeRefusal, type EntryRefusal } from "./memoryEntryIO.ts";

type EntryContent = Extract<PaneContent, { kind: "memoryEntry" }>;

const TYPES = ["user", "feedback", "project", "reference"] as const;
const projectQuery = (project: string) => (project ? "?project=" + encodeURIComponent(project) : "");

/** The draft of a new memory; an existing one is the hook's buffer. */
export interface Draft {
  revision: number;
  kinds: string[];
  name: string;
  description: string;
  type: string;
  body: string;
}

// The drafts of every memory pane, by memory. Never persisted: a page reload starts clean.
const drafts = new Map<string, Draft>();
// The tenant is part of the key: a user-scope memory of one tenant has the same name as another's,
// and a draft carried across a switch would be saved into the wrong workspace.
export const draftKey = (tenant: string, c: { scope: string; project: string; name: string }) => `${tenant}|${c.scope}:${c.project}/${c.name}`;
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
  const trRef = useRef(tr);
  trRef.current = tr;
  const toast = useToast();
  const askConfirm = useConfirm();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const version = useMemoryStore((s) => s.version);
  const bump = useMemoryStore((s) => s.bump);
  const { scope, project, name, gone } = content;
  const isNew = name === "";
  const tenant = useTenantStore((s) => s.tenant);
  const key = draftKey(tenant, content);
  const where = scope === "user" ? tr("mem.af_scope_user") : content.projectName || project;

  const [bodyRaw, setBodyState] = useState<MemoryBody | null>(null);
  const [bodyKey, setBodyKey] = useState("");
  // Only the body read for this key is shown: the first render after a retarget still holds the
  // previous target's.
  const body = bodyRaw && bodyKey === key ? bodyRaw : null;
  const setBody = useCallback(
    (b: MemoryBody | null) => {
      setBodyState(b);
      setBodyKey(b ? key : "");
    },
    [key],
  );
  const [change, setChange] = useState<MemoryChange | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [draft, setDraftState] = useState<Draft | null>(() => initialDraft(key, isNew, !!gone));
  const [formErr, setFormErr] = useState("");
  const [findings, setFindings] = useState<SecretFinding[]>([]);
  const [editing, setEditing] = useState(false);
  const [resolutionOpen, setResolutionOpen] = useState(true);
  const [refusal, setRefusalState] = useState<EntryRefusal | null>(null);
  const onRefused = useCallback((r: EntryRefusal | null) => {
    setRefusalState(r);
    setFindings(r?.findings ?? []);
    setFormErr(r ? (r.code === "memory_secret_detected" ? trRef.current("mem.br_secret") : r.text) : "");
  }, []);
  const io = useMemo(() => memoryEntryIO({ key, scope, project, name }, onRefused), [key, scope, project, name, onRefused]);
  // A body the editor cannot hold (a CR, a NUL) is read-only here, not a render error.
  // The body is the one read for `key`: after a retarget the first render still holds the old
  // target's body, which must not be registered under the new key.
  const { file, fileErr } = useMemo(() => {
    // Unsaved work outlives the body it was made on: a failed re-read, or the memory forgotten
    // elsewhere, must not hand the hook no file, which would leave the buffer's guards with nothing
    // that can save or discard it.
    const held = () => (isNew ? null : heldEditorBuffer(paneId, key));
    if (!body || bodyKey !== key || isNew || gone) return { file: held(), fileErr: "" };
    try {
      // Text a dirty buffer rests on keeps its counter; a clean pane takes the one just read.
      return { file: entryFile(key, body, !dirtyPaneIds().includes(paneId)), fileErr: "" };
    } catch (e) {
      return { file: held(), fileErr: e instanceof Error ? e.message : "" };
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [body, bodyKey, key, isNew, gone, paneId]);
  // Why the body cannot be opened in the editor, when the buffer validator is what refused it.
  const readOnlyWhy =
    fileErr === "too_large" || fileErr === "binary_not_supported" || fileErr === "unsupported_newline" || fileErr === "invalid_unicode"
      ? validationErrText(tr, fileErr)
      : "";
  const editor = useFileEditor(paneId, file, io);
  // A buffer is shown only for the memory this pane is on.
  const model = isNew || editor.model?.path === key ? editor.model : null;
  const phase = model?.phase;
  const alert = isEditorAlert(phase);
  const [busy, setBusy] = useState(false);
  const [purging, setPurging] = useState<PurgeTarget | null>(null);
  const [showHistory, setShowHistory] = useState(false);
  // The toggle sits at the foot of the body, often below the fold of a long memory, and the history
  // opens under it: without this, opening it changes nothing on screen (measured on a 900px window).
  const historyRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (showHistory) historyRef.current?.scrollIntoView?.({ block: "start" });
  }, [showHistory]);
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
  // The pane can be retargeted while mounted (a forget, an unforget, a save of a new memory): what
  // was read for the old target must not be shown, edited or written back for the new one.
  useEffect(() => {
    setDraftState(initialDraft(key, isNew, !!gone));
    // A save refused while this pane was away (its view unmounted) left its reason with the
    // buffer, which is still dirty: show it again rather than a form that says nothing.
    onRefused(!isNew && dirtyPaneIds().includes(paneId) ? refusalOf(key) : null);
    setBody(null);
    setChange(null);
    setLoadErr("");
    setEditing(false);
    setResolutionOpen(true);
  }, [key, isNew, gone]);
  // The save that was refused may have been asked for by a view that is gone by now.
  useEffect(() => (isNew ? undefined : subscribeRefusal(key, onRefused)), [key, isNew, onRefused]);
  useEffect(() => {
    setShowHistory(false);
    setPurging(null);
  }, [key]);

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
        setBody(null);
        setChange(res as MemoryChange);
      } else if (!isMemoryBody(res, scope, name)) {
        setLoadErr(tr("mem.af_failed"));
        setBody(null);
      } else {
        setLoadErr("");
        setChange(null);
        setBody(res as MemoryBody);
      }
      return true;
    },
    [running, isNew, scope, project, name, gone, setBody, tr],
  );
  // The tenant is another workspace: the reset above clears what was read, so it is read again.
  useRetryLoad(load, [load, version, tenant]);

  // A refusal is about the text that was sent: once the buffer reads otherwise (edited since, or
  // the save was refused after the member went on typing), it is no longer shown nor kept.
  const bufferText = model?.content;
  useEffect(() => {
    if (refusal && bufferText !== undefined && bufferText !== refusal.content) {
      onRefused(null);
      clearRefusal(key);
    }
  }, [refusal, bufferText, key, onRefused]);

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
    // Held from the first read until the answer: a second press, or an edit or pin while the
    // confirmation is up, would otherwise race the forget it is about to send.
    inFlight.current = true;
    setBusy(true);
    let latest: (MemoryChange & { error?: { code?: string; message?: string } }) | null = null;
    let ok = false;
    try {
      // Forget is the change list's own revert route, on the newest change of the memory.
      latest = await api(
        `api/agents/memory/entries/member-latest?scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`,
      ).catch(() => null);
      if (!latest || latest.error) toast(latest?.error ? errDetail(latest.error) : tr("mem.af_failed"));
      else if (!latest.commit || !latest.live || !latest.revertible) toast(tr("mem.br_forget_unavailable", { name }));
      else
        ok = await askConfirm({
          title: tr("mem.af_forget_title"),
          body: <p>{tr("mem.af_forget_body", { name, where })}</p>,
          confirmLabel: tr("mem.af_forget"),
          danger: true,
        });
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
    if (!ok || !latest) return;
    const commit = latest.commit;
    await write(
      () =>
        apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(commit), "POST", {
          commit,
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
    if (!model?.dirty) clearPending(key);
    if (!model) {
      // entryFile refused the text (see `file`): say why instead of opening an editor on it.
      setFormErr(readOnlyWhy || tr("mem.af_failed"));
      return;
    }
    setEditing(true);
    setResolutionOpen(true);
    // The buffer may predate a change made elsewhere; a clean one follows the store before it is edited.
    if (!model.dirty) void editor.applyProbeResult({ kind: "revision", revision: file!.revision });
  };

  // What the form shows and edits: the new memory's draft, or the hook's buffer of an existing one.
  const fields = isNew ? draft : !gone && (editing || model?.dirty) ? (model ? { ...decodeEntry(model.content), name } : null) : null;
  const setField = (patch: Partial<Pick<Draft, "description" | "type" | "body">>) => {
    if (isNew) {
      if (draft) setDraft({ ...draft, ...patch });
    } else if (model) {
      editor.edit(encodeEntry({ ...decodeEntry(model.content), ...patch }));
    }
  };

  const saveExisting = async () => {
    if (inFlight.current || !model?.dirty || phase === "saving") return;
    setFormErr("");
    setFindings([]);
    const ok = await editor.save();
    if (!ok) return;
    toast(tr("mem.br_saved", { name }), { kind: "success" });
    setEditing(false);
  };

  const save = async () => {
    if (!isNew) return saveExisting();
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
        // A new memory becomes the pane of the memory it now is; when that memory already has a
        // pane (one memory, one pane), this one closes and that one comes forward.
        if (isNew) {
          const saved = memoryEntryContent({ scope, project: { id: project, display: content.projectName }, name: saveName });
          const st = useLayoutStore.getState();
          const other = allViews(st.layout).find(
            (p) => p.id !== paneId && p.content.kind === "memoryEntry" && p.content.scope === scope && p.content.project === project && p.content.name === saveName,
          );
          if (other) {
            st.setPaneTarget(other.id, { content: saved });
            st.closePane(paneId);
            st.selectTab(other.id);
          } else setPaneTarget(paneId, { content: saved });
        }
      }
    } catch {
      setFormErr(tr("mem.af_failed"));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  const cancel = async () => {
    setFormErr("");
    setFindings([]);
    if (!isNew) {
      if (await editor.discard()) {
        clearPending(key);
        setEditing(false);
        // A remote text adopted during a conflict is newer than the body this pane last read.
        bump();
      }
      return;
    }
    setDraft(null);
    // A new memory that is not saved has nothing to show: its pane closes.
    useLayoutStore.getState().closePane(paneId);
  };

  const who = (m: { authorKind: string; authorSession: string }) =>
    m.authorKind === "member" ? tr("mem.af_by_member") : `${m.authorKind} · ${m.authorSession}`;

  const title = isNew ? (draft?.name.trim() || tr("pane.kind.memory_new")) : name;

  const actions = (
    <>
      {fields ? (
        <>
          <Button icon="save" disabled={busy || phase === "saving" || alert || (!isNew && !model?.dirty)} onClick={() => void save()}>
            {tr("mem.br_save")}
          </Button>
          <Button variant="ghost" disabled={busy || phase === "saving"} onClick={() => void cancel()}>
            {tr("mem.br_cancel")}
          </Button>
        </>
      ) : gone ? (
        change && (
          <>
            {/* Only a forget that can still be reverted brings the memory back; the purge stays
                for one that cannot, whose past text may be exactly what has to go. */}
            {!change.live && change.revertible && (
              <Button icon="discard" disabled={busy} onClick={unforget}>
                {tr("mem.br_unforget")}
              </Button>
            )}
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
            <Button variant="ghost" icon={body.pinned ? "pinned" : "pin"} aria-pressed={!!body.pinned} disabled={busy} onClick={togglePin}>
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
        {body?.pinned && !fields && <span className="mem-badge">{tr("mem.br_pinned")}</span>}
      </ViewHead>
      <div className="mentry-body">
        {!fields && formErr && (
          <p className="mem-warn pad" role="alert">
            {formErr}
          </p>
        )}
        {!running ? (
          <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
            </Button>
          </EmptyState>
        ) : fields ? (
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
                  <input value={fields.name} autoFocus onChange={(e) => draft && setDraft({ ...draft, name: e.target.value })} />
                </label>
              )}
              <label className="mentry-desc">
                {tr("mem.br_f_description")}
                {/* Edit replaces the button that had focus: focus moves here so the keyboard keeps its place. */}
                <input value={fields.description} autoFocus={!isNew} onChange={(e) => setField({ description: e.target.value })} />
              </label>
              <label>
                {tr("mem.br_f_type")}
                <select value={fields.type} onChange={(e) => setField({ type: e.target.value })}>
                  {TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {editor.mergeMine !== null && !isNew && (
              <Button variant="ghost" onClick={() => void navigator.clipboard.writeText(editor.mergeMine!)}>
                {tr("editor.copy_mine")}
              </Button>
            )}
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
                // Fixed: the editor is rebuilt whenever its path changes, which would drop undo on
                // every keystroke in a new memory's name field. The extension picks Markdown.
                path={(name || "memory") + ".md"}
                content={fields.body}
                wrap
                onChange={(text) => setField({ body: text })}
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
              {model?.dirty && (
                <Button variant="ghost" onClick={() => void navigator.clipboard.writeText(model.content)}>
                  {tr("editor.copy_mine")}
                </Button>
              )}
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
            {readOnlyWhy && <p className="muted mentry-readonly-why">{readOnlyWhy}</p>}
            {body.description && <p className="mentry-desc-read">{body.description}</p>}
            <div className="mentry-md">
              <MarkdownView source={body.body} />
            </div>
          </div>
        ) : (
          <p className="muted pad">{tr("common.loading")}</p>
        )}
        {running && !isNew && !fields && (
          <div className="mentry-history" ref={historyRef}>
            <button
              type="button"
              id={`mentry-history-${paneId}`}
              className="linklike"
              aria-expanded={showHistory}
              onClick={() => setShowHistory((v) => !v)}
            >
              <Icon name={showHistory ? "chevron-down" : "chevron-right"} /> {tr("mem.rv_history")}
            </button>
            {showHistory && <EntryHistory scope={scope} project={project} name={name} version={version} labelledBy={`mentry-history-${paneId}`} />}
          </div>
        )}
      </div>
      {!isNew && !gone && model && alert && (
        <EditorResolutionPanel
          model={model}
          open={resolutionOpen}
          onOpen={() => setResolutionOpen(true)}
          onCancel={() => setResolutionOpen(false)}
          onRetryConflict={() => void editor.recoverConflict()}
          onRetryUnknown={() => void editor.recoverUnknown()}
          onResave={() => void editor.resaveUnknown()}
          onRiskAccept={editor.riskAccept}
          // The adopted text is newer than the body this pane last read; back in the read view it would show the old one.
          onTakeRemote={() => {
            editor.takeRemote();
            bump();
          }}
          onDiscardMine={() => {
            editor.discardRemote();
            bump();
          }}
          onManualMerge={editor.manualMerge}
          onCopyMine={() => void navigator.clipboard.writeText(model.content)}
          onClose={() => useLayoutStore.getState().closePane(paneId)}
        />
      )}
      {purging && <PurgeHistoryDialog target={purging} onClose={() => setPurging(null)} onDone={bump} />}
    </div>
  );
}

function blankDraft(): Draft {
  return { revision: 0, kinds: [], name: "", description: "", type: "feedback", body: "" };
}

// A forgotten memory is read-only: a draft left from before the forget is not offered on it.
function initialDraft(key: string, isNew: boolean, gone: boolean): Draft | null {
  if (gone) return null;
  return drafts.get(key) ?? (isNew ? blankDraft() : null);
}

/** The memory's own history, read only while it is shown: 300 changes is not a read to make for a
 *  pane nobody asked the history of, nor again on every write. */
function EntryHistory({
  scope,
  project,
  name,
  version,
  labelledBy,
}: {
  scope: "user" | "project";
  project: string;
  name: string;
  version: number;
  labelledBy: string;
}) {
  const history = useAllChanges(version);
  return <MemoryHistory target={{ scope, project, name }} data={history} onOpenChanges={() => openMemoryView("changes")} labelledBy={labelledBy} />;
}
