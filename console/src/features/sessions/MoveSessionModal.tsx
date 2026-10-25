// MoveSessionModal — "Move to another agent" (ADR 0115). The deterministic sibling of the
// handoff dialog: the Agent renders the source's conversation into a file, starts the target
// kind in the same working copy pointed at that file, and stops the source. No model runs
// before the target's own first turn.
//
// One dialog for the three phases a source can be in (move.ts movePhase), because the row and
// the menu open it the same way whatever the phase:
//   - move:    the preview — eligible kinds, model and driver, and what the user must know
//              before confirming (dirty working copy, queued input, peer messages, running
//              children, file size, the managed-cursor warning). Nothing here is fixed for the
//              user (decision 13): the dirty state is shown, never committed or stashed.
//   - pending: a create that may have started a target (decision 11.4): retry or cancel.
//   - moved:   where the conversation went, a resend of the target's first prompt when its
//              delivery is not known to have arrived, and "resume here anyway" (decision 11.6).
//
// The preview is read without the Agent's gate and can be stale by the time the user confirms;
// the move checks everything again, so a refusal it answers is shown as the result, not guessed
// at here.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { ModelPicker } from "../../ui/ModelPicker.tsx";
import { errText } from "../../core/api/client.ts";
import { agentOf } from "../../agents/registry.ts";
import { useKindCatalogVersion } from "../../agents/kindCatalog.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { getSettings, agentLaunchDefault } from "../../lib/settings.ts";
import { resolveModel } from "../../lib/repoLast.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useSessionsStore } from "./store.ts";
import { openSessionByName } from "./open.ts";
import {
  fmtSize,
  movePhase,
  offeredTargets,
  orderByQuota,
  quotaState,
  type MovePreview,
  type MoveRefusal,
  type MoveDelivery,
  type MoveResult,
  type Answer,
  type MoveUsage,
} from "./move.ts";
import { MOVE_POLL_MS, moveCancel, moveDeliver, movePreview, moveReopen, moveRetry, moveSession } from "./moveApi.ts";
import { useConnectedKinds, useMoveUsage } from "./moveHooks.ts";
import type { Session, SessionKind } from "../../types/session.ts";

interface MoveSessionModalProps {
  session: Session;
  /** Opened from the mirror's limit banner: the kinds with quota left lead the picker and the
   *  first of them is the default (ADR 0115 P2b). Anything else opens it as the menu does. */
  fromLimit?: boolean;
  onClose: () => void;
}

// stale: the last read failed after an earlier one succeeded. The preview it gave is kept, so a
// Workspace that drops one request does not take the dialog's view, and its re-reads, with it.
type Load =
  | { state: "loading" }
  | { state: "error"; error: MoveRefusal | string }
  | { state: "ready"; preview: MovePreview; stale?: MoveRefusal | string };

export function MoveSessionModal({ session, fromLimit = false, onClose }: MoveSessionModalProps) {
  const tr = useT();
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const [busy, setBusy] = useState<"" | "move" | "retry" | "cancel" | "reopen" | "deliver">("");
  const [failure, setFailure] = useState<MoveRefusal | null>(null);
  const [result, setResult] = useState<MoveResult | null>(null);
  const [note, setNote] = useState("");

  // seq counts finished reads: an unchanged preview can come back as the very same object (the
  // api() ETag cache), and the re-read below has to be rescheduled after every read regardless.
  const [seq, setSeq] = useState(0);
  const reload = useCallback(async () => {
    const failed = (error: MoveRefusal | string) =>
      setLoad((l) => (l.state === "ready" ? { ...l, stale: error } : { state: "error", error }));
    try {
      const d = await movePreview(session.name);
      if (d.error) failed(d.error);
      else setLoad({ state: "ready", preview: d });
    } catch (e) {
      failed(String(e));
    } finally {
      setSeq((n) => n + 1);
    }
  }, [session.name]);
  useEffect(() => {
    void reload();
  }, [reload]);

  const refreshList = () => void useSessionsStore.getState().refresh();
  // Every action answers either its own result or a refusal; a refusal is shown and the
  // preview read again, because it usually means the source's state changed underneath.
  const run = async <T,>(what: Exclude<typeof busy, "">, call: () => Promise<Answer<T>>, ok: (d: T) => void) => {
    setBusy(what);
    setFailure(null);
    setNote("");
    try {
      const d = await call();
      if (d.error) setFailure(d.error);
      else ok(d as T);
    } catch (e) {
      setFailure({ message: String(e) });
    } finally {
      setBusy("");
      refreshList();
      void reload();
    }
  };

  const preview = load.state === "ready" ? load.preview : null;
  const phase = result ? "done" : preview ? movePhase(preview) : null;
  // After a move, the source's preview is what says how the target's first prompt is doing: the
  // move's own answer is a snapshot, and a Terminal resend only reports "pending" (202) while
  // the delivery runs in the background and may still fail.
  const resultTarget = result?.target?.name || result?.delivery?.target || "";
  const doneDelivery = result && preview?.movedTo === resultTarget ? preview.delivery : result?.delivery;
  // Some states end without anything the user does: a move another tab (or a request whose
  // answer was lost) is running, and a first prompt being typed into a Terminal target. Read
  // the preview again while one holds, or the buttons stay disabled on a state long gone.
  // A stale preview is one too: it may predate the very action that changed the phase (a
  // move answered move_create_uncertain, a cancel, a reopen), so it is read until a read works.
  const stale = load.state === "ready" && !!load.stale;
  const transient =
    !busy && !!preview && (stale || !!preview.pending?.busy || preview.delivery?.initialPromptState === "pending");
  useEffect(() => {
    if (!transient) return;
    const id = setTimeout(() => void reload(), MOVE_POLL_MS);
    return () => clearTimeout(id);
  }, [transient, seq, reload]);
  const delivered = (d: { initialPromptState: string; mayDuplicate?: boolean }) =>
    setNote(
      tr(
        d.initialPromptState === "pending"
          ? "move.deliver_started"
          : d.mayDuplicate
            ? "move.deliver_sent_maybe_twice"
            : "move.deliver_sent",
      ),
    );

  return (
    <Modal title={tr("move.title", { name: displayName(session) })} onClose={onClose} lockClose={busy !== ""} className="move-modal">
      <div className="ui-modal-body">
        {/* An outcome first, above the view it changed, for the same reason. */}
        {failure && <MoveFailure failure={failure} />}
        {note && (
          <div className="ui-field-hint move-note" role="status">
            {note}
          </div>
        )}
        {load.state === "loading" && !result && (
          <div className="muted move-loading">
            <Icon name="loading" spin /> {tr("move.loading")}
          </div>
        )}
        {load.state === "ready" && load.stale && (
          <div className="ui-field-hint warn move-stale">{tr("move.reread_failed", { reason: errText(load.stale) })}</div>
        )}
        {load.state === "error" && !result && (
          <div className="ui-field-hint warn move-load-error">{tr("move.load_failed", { reason: errText(load.error) })}</div>
        )}
        {result && (
          <MoveDone result={result} delivery={doneDelivery} busy={busy} onDeliver={(t) => run("deliver", () => moveDeliver(t), delivered)} />
        )}
        {!result && preview && phase === "move" && (
          <MoveForm
            session={session}
            preview={preview}
            fromLimit={fromLimit}
            busy={busy === "move"}
            onMove={(kind, model, driver) =>
              run("move", () => moveSession(session.name, kind, model, driver), (d) => setResult(d))
            }
          />
        )}
        {!result && preview && phase === "pending" && (
          <MovePendingView
            preview={preview}
            busy={busy}
            onRetry={() => run("retry", () => moveRetry(session.name), (d) => setResult(d))}
            onCancel={() => run("cancel", () => moveCancel(session.name), () => setNote(tr("move.cancelled")))}
          />
        )}
        {!result && preview && phase === "moved" && (
          <MovedView
            preview={preview}
            busy={busy}
            onDeliver={(t) =>
              run("deliver", () => moveDeliver(t), delivered)
            }
            onReopen={() => run("reopen", () => moveReopen(session.name), () => setNote(tr("move.reopened")))}
          />
        )}
      </div>
      <footer className="ui-modal-foot">
        {/* In the footer, not the body: the body scrolls, and a long preview would keep the
            progress of the one action that takes a while out of sight. */}
        {busy && (
          <div className="move-progress" role="status">
            <Icon name="loading" spin /> {tr(`move.busy_${busy}` as const)}
          </div>
        )}
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose} disabled={busy !== ""}>
          {tr(result || phase !== "move" ? "common.close" : "common.cancel")}
        </button>
        {!result && phase === "move" && (
          <button type="submit" form="move-form" className="ui-btn ui-btn-primary" disabled={busy !== "" || !!preview?.refusal}>
            {tr("move.confirm")}
          </button>
        )}
      </footer>
    </Modal>
  );
}

// --- the move itself -----------------------------------------------------------------------

function MoveForm({
  session,
  preview,
  fromLimit,
  busy,
  onMove,
}: {
  session: Session;
  preview: MovePreview;
  fromLimit: boolean;
  busy: boolean;
  onMove: (kind: SessionKind, model: string, driver: string) => void;
}) {
  const tr = useT();
  const connected = useConnectedKinds();
  const offered = useMemo(() => offeredTargets(preview, connected), [preview, connected]);
  const usage = useMoveUsage(offered);
  // From a limit banner the kinds with quota left lead; a kind with no usage source stays
  // selectable and unmarked (only a kind with a source can be judged).
  const picks = useMemo(() => (fromLimit ? orderByQuota(offered, usage) : offered), [fromLimit, offered, usage]);
  const [kind, setKind] = useState<SessionKind | "">("");
  // Usage arrives after the first paint and reorders the picks: until the user chooses, the
  // default follows the head of the list instead of the head the list had before it.
  const chosen = useRef(false);
  useEffect(() => {
    if (kind && picks.includes(kind) && (chosen.current || !fromLimit)) return;
    setKind(picks[0] ?? "");
  }, [picks, kind, fromLimit]);
  const [model, setModel] = useState("");
  // null = untouched: follows the kind's default, including one that arrives with the kind catalog.
  useKindCatalogVersion();
  const [driverPick, setDriverPick] = useState<string | null>(null);
  const driver = driverPick ?? (kind && agentOf(kind).managedDriver ? "managed" : "");
  // Model and driver default the way the new-session dialog defaults them for this working
  // copy: the per-repo memory, then the user's per-kind default; managed wherever it exists.
  useEffect(() => {
    if (!kind) return;
    setModel(resolveModel(kind, session.repo || "", agentLaunchDefault(getSettings(), kind).model));
    setDriverPick(null);
  }, [kind, session.repo]);

  const notOffered = preview.targets.filter((t) => !offered.includes(t.kind));
  const reasonText = (k: SessionKind, reason?: string): string => {
    if (reason === "same_kind") return tr(preview.canFork ? "move.reason.same_kind_fork" : "move.reason.same_kind");
    if (reason === "cannot_read_file" || reason === "not_measured" || reason === "not_an_agent") return tr(`move.reason.${reason}` as const);
    if (!reason && connected && !connected(k)) return tr("move.reason.not_connected");
    return reason || "";
  };
  const a = kind ? agentOf(kind) : null;
  const children = preview.children ?? [];
  const warnings = preview.warnings ?? [];

  return (
    <form
      id="move-form"
      className="move-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (kind && !busy && !preview.refusal) onMove(kind, a?.caps.model ? model : "", driver);
      }}
    >
      <div className="ui-field-hint">{tr("move.intro", { kind: kindDisplayName(preview.kind) })}</div>
      {preview.refusal && (
        <div className="ui-field-hint warn move-refusal" role="alert">
          <Icon name="warning" /> {refusalText(preview.refusal, tr)}
        </div>
      )}

      <div className="ui-field">
        <span className="ui-field-label">{tr("move.target_label")}</span>
        {offered.length === 0 && <div className="muted launch-noagents">{tr("move.no_targets")}</div>}
        <div className="ui-seg big">
          {picks.map((k) => {
            const ag = agentOf(k);
            const u = usage[k];
            const left = fromLimit && quotaState(u) === "left";
            return (
              <button
                key={k}
                type="button"
                data-kind={k}
                title={tr(ag.launchHintKey)}
                className={"seg-btn kind-" + ag.cssClass + (kind === k ? " active" : "") + (left ? " quota-left" : "")}
                onClick={() => {
                  chosen.current = true;
                  setKind(k);
                }}
              >
                <Icon name={ag.icon} className="seg-ic" />
                {kindDisplayName(k)}
                {u && <span className="seg-sub move-usage">{usageText(u, tr)}</span>}
                {left && <span className="seg-sub move-quota-left">{tr("move.quota_left")}</span>}
              </button>
            );
          })}
        </div>
        {notOffered.length > 0 && (
          <ul className="move-not-offered">
            {notOffered.map((t) => (
              <li key={t.kind} data-kind={t.kind}>
                <span className="move-not-offered-kind">{kindDisplayName(t.kind)}</span>: {reasonText(t.kind, t.reason)}
              </li>
            ))}
          </ul>
        )}
      </div>

      {a?.caps.model && kind && (
        <div className="ui-field">
          <span className="ui-field-label">{tr("launch.field.model")}</span>
          <ModelPicker kind={kind} model={model} onChange={setModel} />
        </div>
      )}
      {a?.managedDriver && a.terminalDriver !== false && (
        <div className="ui-field">
          <span className="ui-field-label">{tr("launch.field.driver")}</span>
          <div className="ui-seg">
            <button type="button" className={"seg-btn" + (driver === "managed" ? " active" : "")} onClick={() => setDriverPick("managed")}>
              <Icon name="server-process" /> {tr("launch.driver_managed")}
            </button>
            <button type="button" className={"seg-btn" + (driver !== "managed" ? " active" : "")} onClick={() => setDriverPick("")}>
              <Icon name="terminal" /> {tr("launch.driver_terminal")}
            </button>
          </div>
        </div>
      )}

      {warnings.includes("managed_cursor_mirror_empty") && (
        <div className="ui-field-hint warn move-warn-cursor">
          <Icon name="warning" /> {tr("move.warn.managed_cursor_mirror_empty")}
        </div>
      )}

      <MoveGitSection preview={preview} />

      {(preview.queued?.length ?? 0) > 0 && (
        <div className="ui-field move-queued">
          <span className="ui-field-label">{tr("move.queued_label", { n: preview.queued!.length })}</span>
          <div className="ui-field-hint">{tr("move.queued_hint")}</div>
          <ul className="move-list">
            {preview.queued!.map((q, i) => (
              <li key={i}>{q.text}</li>
            ))}
          </ul>
        </div>
      )}
      {(preview.peers?.length ?? 0) > 0 && (
        <div className="ui-field move-peers">
          <span className="ui-field-label">{tr("move.peers_label", { n: preview.peers!.length })}</span>
          <div className="ui-field-hint">{tr("move.peers_hint")}</div>
          <ul className="move-list">
            {preview.peers!.map((p, i) => (
              <li key={i}>
                <b>{p.from}</b>
                {p.intent ? ` (${p.intent})` : ""}: {p.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      {(children.length > 0 || (preview.proposals?.length ?? 0) > 0) && (
        <div className="ui-field move-children">
          <span className="ui-field-label">{tr("move.children_label")}</span>
          {warnings.includes("children_running") && (
            <div className="ui-field-hint warn move-warn-children">
              <Icon name="warning" /> {tr("move.warn.children_running")}
            </div>
          )}
          <div className="ui-field-hint">{tr("move.children_hint")}</div>
          <ul className="move-list">
            {children.map((c) => (
              <li key={c.name}>
                {c.name} — {c.state}
              </li>
            ))}
            {(preview.proposals ?? []).map((p) => (
              <li key={"p-" + p}>{tr("move.proposal_item", { name: p })}</li>
            ))}
          </ul>
        </div>
      )}
      {!!preview.sizeBytes && (
        <div className="ui-field-hint move-size">
          {tr("move.size", { size: fmtSize(preview.sizeBytes) })}
          {preview.overSizeTarget && <> {tr("move.size_over")}</>}
        </div>
      )}
    </form>
  );
}

function MoveGitSection({ preview }: { preview: MovePreview }) {
  const tr = useT();
  const g = preview.git;
  if (!g) return null;
  return (
    <div className="ui-field move-git">
      <span className="ui-field-label">{tr("move.git_label")}</span>
      {!g.available ? (
        <div className="ui-field-hint">{tr("move.git_unavailable")}</div>
      ) : g.dirty ? (
        <>
          <div className="ui-field-hint warn move-git-dirty">
            <Icon name="warning" /> {tr("move.git_dirty")}
          </div>
          <pre className="move-pre">{[g.status, g.diffStat].filter(Boolean).join("\n\n")}</pre>
        </>
      ) : (
        <div className="ui-field-hint move-git-clean">{tr("move.git_clean")}</div>
      )}
      {g.available && g.ahead && (
        <>
          <div className="ui-field-hint">{tr("move.git_ahead")}</div>
          <pre className="move-pre">{g.ahead}</pre>
        </>
      )}
    </div>
  );
}

function usageText(u: MoveUsage, tr: ReturnType<typeof useT>): string {
  const parts: string[] = [];
  if (u.fiveHour !== undefined) parts.push(tr("move.usage_five", { pct: u.fiveHour }));
  if (u.sevenDay !== undefined) parts.push(tr("move.usage_week", { pct: u.sevenDay }));
  if (u.monthly !== undefined) parts.push(tr("move.usage_month", { pct: u.monthly }));
  return parts.join(" · ");
}

// --- a move to finish, and a move done -----------------------------------------------------

function MovePendingView({
  preview,
  busy,
  onRetry,
  onCancel,
}: {
  preview: MovePreview;
  busy: string;
  onRetry: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const p = preview.pending;
  const target = p?.target || preview.moving || "";
  const running = !!p?.busy;
  return (
    <div className="move-pending">
      <div className="ui-field-hint warn" role="alert">
        <Icon name="warning" /> {tr(running ? "move.pending_running" : "move.pending_intro", { target })}
      </div>
      {p?.error && <div className="ui-field-hint move-pending-error">{tr("move.pending_error", { reason: p.error })}</div>}
      {p?.orphan && <div className="ui-field-hint">{tr("move.pending_orphan")}</div>}
      {p?.targetExists && (
        <div className="ui-field-hint">
          {tr("move.pending_target_exists")}{" "}
          <OpenSessionLink name={target} />
        </div>
      )}
      <div className="move-actions">
        <button type="button" className="ui-btn ui-btn-primary move-retry" disabled={!!busy || running || !!p?.orphan} onClick={onRetry}>
          {tr("move.retry")}
        </button>
        <button type="button" className="ui-btn move-cancel" disabled={!!busy || running || !!p?.targetExists} onClick={onCancel}>
          {tr("move.cancel_move")}
        </button>
      </div>
      <div className="ui-field-hint">{tr("move.pending_hint")}</div>
    </div>
  );
}

function MovedView({
  preview,
  busy,
  onDeliver,
  onReopen,
}: {
  preview: MovePreview;
  busy: string;
  onDeliver: (target: string) => void;
  onReopen: () => void;
}) {
  const tr = useT();
  const target = preview.movedTo!;
  return (
    <div className="move-moved">
      <div className="ui-field-hint move-moved-to">
        <Icon name="arrow-right" /> {tr("move.moved_to", { target: sessionLabel(target) })} <OpenSessionLink name={target} />
      </div>
      {preview.delivery && <DeliveryRow delivery={preview.delivery} busy={busy} onDeliver={() => onDeliver(preview.delivery!.target)} />}
      <div className="ui-field move-reopen">
        <span className="ui-field-label">{tr("move.reopen_label")}</span>
        <div className="ui-field-hint">{tr("move.reopen_hint", { target: sessionLabel(target) })}</div>
        <div className="move-actions">
          <button type="button" className="ui-btn move-reopen-btn" disabled={!!busy} onClick={onReopen}>
            {tr("move.reopen")}
          </button>
        </div>
      </div>
    </div>
  );
}

function MoveDone({
  result,
  delivery,
  busy,
  onDeliver,
}: {
  result: MoveResult;
  delivery?: MoveDelivery;
  busy: string;
  onDeliver: (target: string) => void;
}) {
  const tr = useT();
  const target = result.target?.name || result.delivery?.target || "";
  return (
    <div className="move-done" role="status">
      <div className="ui-field-hint move-done-head">
        <Icon name="check" /> {tr("move.done", { target: result.target ? displayName(result.target) : target })}{" "}
        {target && <OpenSessionLink name={target} />}
      </div>
      <div className="ui-field-hint">{tr("move.done_file", { file: result.file, size: fmtSize(result.sizeBytes ?? 0) })}</div>
      {!!result.cut && <div className="ui-field-hint">{tr("move.done_cut", { n: result.cut })}</div>}
      {!result.sourceStopped && (
        <div className="ui-field-hint warn">{tr("move.done_not_stopped", { reason: result.stopError || "" })}</div>
      )}
      {delivery && <DeliveryRow delivery={delivery} busy={busy} onDeliver={() => onDeliver(delivery.target)} />}
    </div>
  );
}

// DeliveryRow: the target exists but its first prompt, the one that points at the file, is not
// known to have arrived. Resent only on the user's word: an unknown delivery may have arrived.
function DeliveryRow({ delivery, busy, onDeliver }: { delivery: { target: string; initialPromptState: string; deliverable: boolean }; busy: string; onDeliver: () => void }) {
  const tr = useT();
  const st = delivery.initialPromptState;
  const key = st === "pending" ? "move.delivery_pending" : st === "failed" ? "move.delivery_failed" : "move.delivery_unknown";
  return (
    <div className="ui-field move-delivery">
      <div className="ui-field-hint warn">
        <Icon name="warning" /> {tr(key, { target: sessionLabel(delivery.target) })}
      </div>
      {delivery.deliverable && (
        <div className="move-actions">
          <button type="button" className="ui-btn move-deliver" disabled={!!busy} onClick={onDeliver}>
            {tr("move.deliver")}
          </button>
        </div>
      )}
    </div>
  );
}

function MoveFailure({ failure }: { failure: MoveRefusal }) {
  const tr = useT();
  return (
    <div className="ui-field-hint warn move-failure" role="alert">
      <Icon name="warning" /> {refusalText(failure, tr)}
    </div>
  );
}

function OpenSessionLink({ name }: { name: string }) {
  const tr = useT();
  if (!name) return null;
  return (
    <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm move-open-target" onClick={() => openSessionByName(name)}>
      <Icon name="link-external" /> {tr("move.open_target")}
    </button>
  );
}

// refusalText words a refusal: the catalogue's text for its code, with the state a
// move_source_state names and the session a refusal points at.
function refusalText(e: MoveRefusal, tr: ReturnType<typeof useT>): string {
  let s = errText(e);
  if (e.code === "move_source_state" && e.state) s += " " + tr("move.source_state", { state: e.state });
  if (e.target && e.code !== "move_source_state") s += " " + tr("move.refusal_target", { target: sessionLabel(e.target) });
  return s;
}

// sessionLabel: a session's display name when the list has it, its id otherwise (a target the
// next list refresh has not brought in yet).
function sessionLabel(name: string): string {
  const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
  return s ? displayName(s) : name;
}
