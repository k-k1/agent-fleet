import { requireRevision, revisionOf, validateEditorBuffer } from "./buffer.ts";
import { checkSuggestion, type EditSuggestionEnvelope } from "./suggest.ts";

export type EditorPhase =
  | "clean"
  | "dirty"
  | "saving"
  | "saved"
  | "clean_risk_accepted"
  | "save_state_unknown"
  | "conflict"
  | "conflict_remote_unavailable";

export interface SaveSnapshot {
  paneId: string;
  path: string;
  bufferGeneration: number;
  bufferRevision: string;
  content: string;
  baseDiskRevision: string;
}

export interface RemoteSnapshot {
  path: string;
  content: string;
  revision: string;
  fetchedAt: number;
}

export function createRemoteSnapshot(
  path: string,
  content: string,
  revision: string,
  fetchedAt: number,
): RemoteSnapshot {
  const validRevision = requireRevision(revision);
  if (revisionOf(content) !== validRevision) throw new Error("revision/content mismatch");
  if (!Number.isFinite(fetchedAt) || fetchedAt < 0) throw new Error("invalid fetchedAt");
  return { path, content, revision: validRevision, fetchedAt };
}

export type UnknownObservation =
  | { kind: "sent_live"; remote: RemoteSnapshot }
  | { kind: "old_base_live"; remote: RemoteSnapshot }
  | { kind: "third_revision"; remote: RemoteSnapshot }
  | { kind: "unavailable"; message: string };

/** Advisory record of what the external-change probe observed (docs/log/44 §7.3).
 *  It lives beside `phase` and never drives it: `conflict` still only arises
 *  from a 409 or from the user's explicit diff-check action. `same_as_buffer`
 *  is the non-conflict case where an external writer produced exactly the
 *  unsaved buffer content (§7.3, classified like classifyUnknownRemote). */
export type ExternalObservation =
  | { kind: "changed"; revision: string }
  | { kind: "same_as_buffer"; revision: string }
  | { kind: "missing" }
  | { kind: "uneditable"; reason: string }
  | { kind: "boundary" };

export const CLEAN_PHASES: readonly EditorPhase[] = ["clean", "saved", "clean_risk_accepted"];

export interface FileEditorModel {
  paneId: string;
  path: string;
  content: string;
  bufferGeneration: number;
  bufferRevision: string;
  baseDiskContent: string;
  baseDiskRevision: string;
  dirty: boolean;
  phase: EditorPhase;
  saveSnapshot: SaveSnapshot | null;
  conflict: RemoteSnapshot | null;
  unknownObservation: UnknownObservation | null;
  externalObservation: ExternalObservation | null;
  /** Bumped by each clean auto-follow. The editor surface keys its state
   *  rebuild on it, which is what keeps the replaced content out of the undo
   *  history (docs/log/44 §7.4). */
  followEpoch: number;
  riskAccepted: boolean;
  message: string;
  /** Pending AI edit suggestion (docs/log/44 §4). Advisory beside `phase` like
   *  `externalObservation`: it never drives `phase`, and staleness is DERIVED
   *  (suggestion.baseRevision !== bufferRevision) rather than stored, so a
   *  buffer edit during review makes it stale without extra transitions.
   *  Accept re-checks the revision triple; a stale accept never applies. */
  suggestion: EditSuggestionEnvelope | null;
}

export function createFileEditorModel(
  paneId: string,
  path: string,
  content: string,
  revision: string,
): FileEditorModel {
  const error = validateEditorBuffer(content);
  if (error) throw new Error(error.code);
  const validRevision = requireRevision(revision);
  if (revisionOf(content) !== validRevision) throw new Error("revision/content mismatch");
  return {
    paneId,
    path,
    content,
    bufferGeneration: 0,
    bufferRevision: validRevision,
    baseDiskContent: content,
    baseDiskRevision: validRevision,
    dirty: false,
    phase: "clean",
    saveSnapshot: null,
    conflict: null,
    unknownObservation: null,
    externalObservation: null,
    followEpoch: 0,
    riskAccepted: false,
    message: "",
    suggestion: null,
  };
}

export function editBuffer(model: FileEditorModel, content: string): FileEditorModel {
  const error = validateEditorBuffer(content);
  if (error) throw new Error(error.code);
  const phase =
    model.phase === "saving" ||
    model.phase === "save_state_unknown" ||
    model.phase === "conflict" ||
    model.phase === "conflict_remote_unavailable"
      ? model.phase
      : "dirty";
  return {
    ...model,
    content,
    bufferGeneration: model.bufferGeneration + 1,
    bufferRevision: revisionOf(content),
    dirty: true,
    phase,
    message: "",
  };
}

export function beginSave(model: FileEditorModel): [FileEditorModel, SaveSnapshot] {
  // SaveStateUnknown must first pass through the explicit re-save action, which
  // installs an observed base revision and returns the model to Dirty. A generic
  // Save shortcut must never turn state recovery into an implicit retry.
  if (!model.dirty || model.phase !== "dirty") throw new Error("save not available");
  const snapshot: SaveSnapshot = {
    paneId: model.paneId,
    path: model.path,
    bufferGeneration: model.bufferGeneration,
    bufferRevision: requireRevision(model.bufferRevision),
    content: model.content,
    baseDiskRevision: requireRevision(model.baseDiskRevision),
  };
  return [{ ...model, phase: "saving", saveSnapshot: snapshot, message: "" }, snapshot];
}

export function saveSucceeded(
  model: FileEditorModel,
  snapshot: SaveSnapshot,
  responseRevision: string,
): FileEditorModel {
  const revision = requireRevision(responseRevision);
  if (revision !== snapshot.bufferRevision) throw new Error("save response revision mismatch");
  const unchanged =
    model.bufferGeneration === snapshot.bufferGeneration ||
    model.bufferRevision === snapshot.bufferRevision;
  return {
    ...model,
    baseDiskContent: snapshot.content,
    baseDiskRevision: revision,
    dirty: !unchanged,
    phase: unchanged ? "saved" : "dirty",
    saveSnapshot: null,
    // A 200 means the CAS compared equal at write time, so whatever the probe
    // had observed was already resolved or stale.
    externalObservation: null,
    message: "",
  };
}

export function saveFailed(model: FileEditorModel, message: string): FileEditorModel {
  return { ...model, phase: "dirty", message, saveSnapshot: null, dirty: true };
}

export function saveStateUnknown(
  model: FileEditorModel,
  snapshot: SaveSnapshot,
  message: string,
): FileEditorModel {
  return {
    ...model,
    dirty: true,
    phase: "save_state_unknown",
    saveSnapshot: snapshot,
    unknownObservation: null,
    message,
  };
}

export function conflictFound(
  model: FileEditorModel,
  remote: RemoteSnapshot,
): FileEditorModel {
  return {
    ...model,
    dirty: true,
    phase: "conflict",
    conflict: { ...remote, revision: requireRevision(remote.revision) },
    saveSnapshot: null,
    unknownObservation: null,
    externalObservation: null,
    message: "",
  };
}

export function conflictUnavailable(model: FileEditorModel, message: string): FileEditorModel {
  return {
    ...model,
    dirty: true,
    phase: "conflict_remote_unavailable",
    conflict: null,
    message,
  };
}

export function observeUnknown(
  model: FileEditorModel,
  observation: UnknownObservation,
): FileEditorModel {
  if (observation.kind === "third_revision") return conflictFound(model, observation.remote);
  return {
    ...model,
    phase: "save_state_unknown",
    unknownObservation: observation,
    message: observation.kind === "unavailable" ? observation.message : "",
  };
}

export function classifyUnknownRemote(
  snapshot: SaveSnapshot,
  remote: RemoteSnapshot,
): UnknownObservation {
  if (remote.revision === snapshot.bufferRevision) return { kind: "sent_live", remote };
  if (remote.revision === snapshot.baseDiskRevision) return { kind: "old_base_live", remote };
  return { kind: "third_revision", remote };
}

export function acceptUnknownRisk(model: FileEditorModel): FileEditorModel {
  const observation = model.unknownObservation;
  const snapshot = model.saveSnapshot;
  if (!snapshot || observation?.kind !== "sent_live") throw new Error("risk acceptance unavailable");
  const unchanged =
    model.bufferGeneration === snapshot.bufferGeneration ||
    model.bufferRevision === snapshot.bufferRevision;
  return {
    ...model,
    baseDiskContent: observation.remote.content,
    baseDiskRevision: observation.remote.revision,
    dirty: !unchanged,
    phase: unchanged ? "clean_risk_accepted" : "dirty",
    saveSnapshot: null,
    unknownObservation: null,
    riskAccepted: true,
    message: "",
  };
}

export function prepareUnknownResave(model: FileEditorModel): FileEditorModel {
  const observation = model.unknownObservation;
  if (!observation || observation.kind === "unavailable" || observation.kind === "third_revision") {
    throw new Error("re-save unavailable");
  }
  return {
    ...model,
    baseDiskContent: observation.remote.content,
    baseDiskRevision: observation.remote.revision,
    phase: "dirty",
    saveSnapshot: null,
    unknownObservation: null,
    dirty: true,
    message: "",
  };
}

export function adoptRemote(model: FileEditorModel, dirty = false): FileEditorModel {
  if (!model.conflict) throw new Error("remote unavailable");
  return {
    ...model,
    content: model.conflict.content,
    bufferGeneration: model.bufferGeneration + 1,
    bufferRevision: model.conflict.revision,
    baseDiskContent: model.conflict.content,
    baseDiskRevision: model.conflict.revision,
    dirty,
    phase: dirty ? "dirty" : "clean",
    conflict: null,
    saveSnapshot: null,
    externalObservation: null,
    message: "",
  };
}

export function startManualMerge(model: FileEditorModel): FileEditorModel {
  return adoptRemote(model, true);
}

export function discardToBase(model: FileEditorModel): FileEditorModel {
  const observed =
    model.conflict ||
    (model.unknownObservation?.kind !== "unavailable"
      ? model.unknownObservation?.remote
      : null);
  if (
    model.phase === "saving" ||
    (model.phase === "save_state_unknown" && !observed) ||
    (model.phase === "conflict" && !observed) ||
    model.phase === "conflict_remote_unavailable"
  ) {
    throw new Error("discard target unavailable");
  }
  const content = observed?.content ?? model.baseDiskContent;
  const revision = observed?.revision ?? model.baseDiskRevision;
  const clean = createFileEditorModel(
    model.paneId,
    model.path,
    content,
    revision,
  );
  // The follow epoch is preserved: a discard replaces the buffer through the
  // ordinary content sync and stays undoable, unlike an auto-follow.
  return { ...clean, bufferGeneration: model.bufferGeneration + 1, followEpoch: model.followEpoch };
}

/** Record (or clear) the pending AI suggestion (docs/log/44 §4). Advisory only:
 *  `phase`, the buffer, and every base revision stay untouched. The caller
 *  validates the envelope (checkSuggestion) before recording it. */
export function setSuggestion(
  model: FileEditorModel,
  suggestion: EditSuggestionEnvelope | null,
): FileEditorModel {
  return { ...model, suggestion };
}

/** Accept the pending suggestion into the buffer (docs/log/44 §4.2/§4.3): re-check
 *  identity, the revision triple, the range, and the shared buffer validator
 *  against the CURRENT buffer, then apply through editBuffer — the buffer goes
 *  dirty and nothing here touches PUT. Throws the stable UI code
 *  (`suggestion_stale` etc.) when the check fails; the suggestion is retired
 *  either way only on success. */
export function applySuggestion(model: FileEditorModel): FileEditorModel {
  if (!model.suggestion) throw new Error("suggestion_invalid");
  const check = checkSuggestion(model.suggestion, model);
  if (!check.ok) throw new Error(check.code);
  return { ...editBuffer(model, check.applied), suggestion: null };
}

/** Record (or clear) what the probe observed. Advisory only: `phase`, the
 *  buffer, and every base revision stay untouched (docs/log/44 §7.3). */
export function observeExternal(
  model: FileEditorModel,
  observation: ExternalObservation | null,
): FileEditorModel {
  return { ...model, externalObservation: observation };
}

/** Adopt an externally changed file into a clean buffer (docs/log/44 §7.4). The
 *  model is rebuilt from the remote snapshot — not edited — and the follow
 *  epoch tells the editor surface to rebuild its state so undo cannot roll the
 *  external change back. A risk-accepted clean base is followed too, and the
 *  follow retires the risk marker. */
export function followExternal(
  model: FileEditorModel,
  remote: RemoteSnapshot,
): FileEditorModel {
  if (model.dirty || !CLEAN_PHASES.includes(model.phase)) throw new Error("follow unavailable");
  const clean = createFileEditorModel(model.paneId, model.path, remote.content, remote.revision);
  return {
    ...clean,
    bufferGeneration: model.bufferGeneration + 1,
    followEpoch: model.followEpoch + 1,
  };
}
