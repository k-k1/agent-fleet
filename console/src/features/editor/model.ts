import { requireRevision, revisionOf, validateEditorBuffer } from "./buffer.ts";

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
}

export type UnknownObservation =
  | { kind: "sent_live"; remote: RemoteSnapshot }
  | { kind: "old_base_live"; remote: RemoteSnapshot }
  | { kind: "third_revision"; remote: RemoteSnapshot }
  | { kind: "unavailable"; message: string };

export interface FileEditorModel {
  paneId: string;
  path: string;
  content: string;
  bufferGeneration: number;
  bufferRevision: string;
  baseDiskRevision: string;
  dirty: boolean;
  phase: EditorPhase;
  saveSnapshot: SaveSnapshot | null;
  conflict: RemoteSnapshot | null;
  unknownObservation: UnknownObservation | null;
  riskAccepted: boolean;
  message: string;
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
    baseDiskRevision: validRevision,
    dirty: false,
    phase: "clean",
    saveSnapshot: null,
    conflict: null,
    unknownObservation: null,
    riskAccepted: false,
    message: "",
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
    model.bufferGeneration === snapshot.bufferGeneration &&
    model.bufferRevision === snapshot.bufferRevision;
  return {
    ...model,
    baseDiskRevision: revision,
    dirty: !unchanged,
    phase: unchanged ? "saved" : "dirty",
    saveSnapshot: null,
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
    model.bufferGeneration === snapshot.bufferGeneration &&
    model.bufferRevision === snapshot.bufferRevision;
  return {
    ...model,
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
    baseDiskRevision: model.conflict.revision,
    dirty,
    phase: dirty ? "dirty" : "clean",
    conflict: null,
    saveSnapshot: null,
    message: "",
  };
}

export function startManualMerge(model: FileEditorModel): FileEditorModel {
  return adoptRemote(model, true);
}

export function discardToBase(model: FileEditorModel, content: string, revision: string): FileEditorModel {
  const clean = createFileEditorModel(model.paneId, model.path, content, revision);
  return { ...clean, bufferGeneration: model.bufferGeneration + 1 };
}
