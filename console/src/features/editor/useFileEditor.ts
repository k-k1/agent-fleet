import { useCallback, useEffect, useRef, useState } from "react";
import {
  getEditableFile,
  putFile,
  suggestEdit,
  type EditableFile,
  type FileProbeResult,
} from "./api.ts";
import {
  CLEAN_PHASES,
  acceptUnknownRisk,
  adoptRemote,
  applySuggestion,
  beginSave,
  classifyUnknownRemote,
  conflictFound,
  conflictUnavailable,
  createRemoteSnapshot,
  createFileEditorModel,
  discardToBase,
  editBuffer,
  followExternal,
  observeExternal,
  observeUnknown,
  prepareUnknownResave,
  saveFailed,
  saveStateUnknown,
  saveSucceeded,
  setSuggestion,
  startManualMerge,
  type FileEditorModel,
  type RemoteSnapshot,
  type SaveSnapshot,
} from "./model.ts";
import {
  checkSuggestion,
  suggestWindows,
  SUGGEST_MAX_INSTRUCTION_BYTES,
  type EditRange,
  type EditSuggestionEnvelope,
} from "./suggest.ts";
import { RecoveryCoordinator } from "./recovery.ts";
import {
  notifyDirtyEditorChanged,
  registerDirtyEditor,
} from "./dirtyRegistry.ts";
import { errText } from "../../core/api/client.ts";

interface InitialEditableFile {
  path: string;
  content: string;
  revision: string;
}

const isCleanForFollow = (model: FileEditorModel): boolean =>
  !model.dirty && CLEAN_PHASES.includes(model.phase);

// Identifier for a suggestion request (docs/log/44 §4.1 requestId). It only has to
// identify which response to merge where, so a counter monotonic within the session is
// enough.
let suggestSeq = 0;

function remoteFromFile(
  file: Awaited<ReturnType<typeof getEditableFile>>,
  fetchedAt = Date.now(),
): RemoteSnapshot {
  return createRemoteSnapshot(
    file.path,
    file.content,
    file.revision,
    fetchedAt,
  );
}

export function useFileEditor(paneId: string, initial: InitialEditableFile | null) {
  const [model, setReactModel] = useState<FileEditorModel | null>(() =>
    initial ? createFileEditorModel(paneId, initial.path, initial.content, initial.revision) : null,
  );
  const modelRef = useRef(model);
  const savingRef = useRef<Promise<boolean> | null>(null);
  const mergeMineRef = useRef<string | null>(null);
  const mountedRef = useRef(true);
  const recoveryRef = useRef<RecoveryCoordinator | null>(null);
  if (!recoveryRef.current) recoveryRef.current = new RecoveryCoordinator();

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      recoveryRef.current?.invalidate();
    };
  }, []);

  const setModel = useCallback((next: FileEditorModel) => {
    if (!mountedRef.current) return;
    modelRef.current = next;
    setReactModel(next);
    notifyDirtyEditorChanged();
  }, []);

  useEffect(() => {
    if (!initial) {
      recoveryRef.current?.invalidate();
      modelRef.current = null;
      setReactModel(null);
      return;
    }
    const current = modelRef.current;
    if (current?.path === initial.path) return;
    recoveryRef.current?.invalidate();
    const next = createFileEditorModel(paneId, initial.path, initial.content, initial.revision);
    modelRef.current = next;
    setReactModel(next);
  }, [initial, paneId]);

  const recoverConflict = useCallback((): Promise<void> => {
    const current = modelRef.current;
    if (!current) return Promise.resolve();
    const path = current.path;
    const baseDiskRevision = current.baseDiskRevision;
    return recoveryRef.current!.run(
      `conflict:${path}:${baseDiskRevision}`,
      async () => {
        try {
          const remote = remoteFromFile(await getEditableFile(path));
          if (remote.path !== path) throw new Error("path mismatch");
          return { ok: true as const, remote };
        } catch (error) {
          return { ok: false as const, message: String(error) };
        }
      },
      (result) => {
        const latest = modelRef.current;
        if (
          !latest ||
          latest.path !== path ||
          latest.baseDiskRevision !== baseDiskRevision ||
          !["saving", "conflict", "conflict_remote_unavailable"].includes(latest.phase)
        ) return;
        setModel(
          result.ok
            ? conflictFound(latest, result.remote)
            : conflictUnavailable(latest, result.message),
        );
      },
    );
  }, [setModel]);

  const recoverUnknown = useCallback((snapshot?: SaveSnapshot): Promise<void> => {
    const current = modelRef.current;
    const sent = snapshot || current?.saveSnapshot;
    if (!current || !sent) return Promise.resolve();
    const path = current.path;
    const snapshotKey = [
      sent.bufferGeneration,
      sent.bufferRevision,
      sent.baseDiskRevision,
    ].join(":");
    return recoveryRef.current!.run(
      `unknown:${path}:${snapshotKey}`,
      async () => {
        try {
          const remote = remoteFromFile(await getEditableFile(path));
          if (remote.path !== path) throw new Error("path mismatch");
          return { ok: true as const, remote };
        } catch (error) {
          return { ok: false as const, message: String(error) };
        }
      },
      (result) => {
        const latest = modelRef.current;
        const latestSnapshot = latest?.saveSnapshot;
        if (
          !latest ||
          latest.path !== path ||
          !latestSnapshot ||
          latestSnapshot.path !== sent.path ||
          latestSnapshot.bufferGeneration !== sent.bufferGeneration ||
          latestSnapshot.bufferRevision !== sent.bufferRevision ||
          latestSnapshot.baseDiskRevision !== sent.baseDiskRevision
        ) return;
        setModel(
          observeUnknown(
            latest,
            result.ok
              ? classifyUnknownRemote(sent, result.remote)
              : { kind: "unavailable", message: result.message },
          ),
        );
      },
    );
  }, [setModel]);

  const save = useCallback(async (): Promise<boolean> => {
    if (savingRef.current) return savingRef.current;
    const current = modelRef.current;
    if (!current || !current.dirty) return true;
    let saving: FileEditorModel;
    let snapshot: SaveSnapshot;
    try {
      [saving, snapshot] = beginSave(current);
    } catch {
      return false;
    }
    setModel(saving);
    const operation = (async () => {
      try {
        const result = await putFile(snapshot.path, snapshot.content, snapshot.baseDiskRevision);
        if (result.ok) {
          try {
            if (result.path !== snapshot.path) throw new Error("save response path mismatch");
            const next = saveSucceeded(modelRef.current!, snapshot, result.revision);
            if (!next.dirty) mergeMineRef.current = null;
            setModel(next);
          } catch (error) {
            setModel(saveStateUnknown(modelRef.current!, snapshot, String(error)));
            await recoverUnknown(snapshot);
          }
        } else if (result.status === 409 && result.error.code === "revision_conflict") {
          await recoverConflict();
        } else if (
          result.error.code === "write_state_unknown" ||
          /^http_5\d\d$/.test(result.error.code)
        ) {
          setModel(saveStateUnknown(modelRef.current!, snapshot, result.error.message));
          await recoverUnknown(snapshot);
        } else {
          setModel(saveFailed(modelRef.current!, errText(result.error)));
        }
      } catch (error) {
        // A thrown transport failure may have lost a response after the Agent
        // committed the rename. It is never treated as an ordinary failed save.
        setModel(saveStateUnknown(modelRef.current!, snapshot, String(error)));
        await recoverUnknown(snapshot);
      }
      return !modelRef.current?.dirty;
    })();
    savingRef.current = operation;
    try {
      return await operation;
    } finally {
      savingRef.current = null;
    }
  }, [recoverConflict, recoverUnknown, setModel]);

  const edit = useCallback((content: string) => {
    const current = modelRef.current;
    if (!current || current.content === content) return;
    setModel(editBuffer(current, content));
  }, [setModel]);

  const discard = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    const requested = modelRef.current;
    if (!requested) return true;
    if (signal?.aborted) return false;
    const path = requested.path;

    try {
      // A PUT cannot be cancelled after the Agent may have committed its rename.
      // Wait for its complete save/recovery path before choosing the disk-backed
      // discard target. The waits themselves are cancellable: when the guard
      // request aborts (Back/cancel while this discard is pending), the
      // navigation was already refused, so the buffer must never be cleaned.
      if (savingRef.current) await savingRef.current;
      if (signal?.aborted) return false;
      let current = modelRef.current;
      if (!current || current.path !== path) return false;

      if (
        current.phase === "save_state_unknown" &&
        (!current.unknownObservation || current.unknownObservation.kind === "unavailable")
      ) {
        await recoverUnknown();
      } else if (current.phase === "conflict_remote_unavailable") {
        await recoverConflict();
      }
      if (signal?.aborted) return false;
      current = modelRef.current;
      if (!current || current.path !== path) return false;

      mergeMineRef.current = null;
      setModel(discardToBase(current));
      return true;
    } catch {
      // Without a verified live snapshot, marking the buffer clean would invent
      // a disk base. Keep the guard open so the user can retry or cancel.
      return false;
    }
  }, [recoverConflict, recoverUnknown, setModel]);

  useEffect(() => {
    if (!initial) return;
    return registerDirtyEditor({
      paneId,
      label: initial.path,
      isDirty: () => !!modelRef.current?.dirty,
      save,
      discard,
    });
  }, [discard, initial, paneId, save]);

  const resaveUnknown = useCallback(async () => {
    const current = modelRef.current;
    if (!current) return false;
    try {
      setModel(prepareUnknownResave(current));
      return await save();
    } catch {
      return false;
    }
  }, [save, setModel]);

  const riskAccept = useCallback(() => {
    const current = modelRef.current;
    if (current) {
      recoveryRef.current?.invalidate();
      setModel(acceptUnknownRisk(current));
    }
  }, [setModel]);

  const takeRemote = useCallback(() => {
    const current = modelRef.current;
    if (current?.conflict) {
      recoveryRef.current?.invalidate();
      mergeMineRef.current = null;
      setModel(adoptRemote(current));
    }
  }, [setModel]);

  // Clean auto-follow (docs/log/44 §7.4): the probe saw a new disk revision and the
  // buffer holds no edits, so fetch the full body and rebuild the model on it.
  // Returns the fetched file when the model actually followed, so the caller
  // can refresh its own copy of the pane data from the same response.
  const followExternalChange = useCallback(async (): Promise<EditableFile | null> => {
    const current = modelRef.current;
    if (!current || savingRef.current || !isCleanForFollow(current)) return null;
    const path = current.path;
    try {
      const file = await getEditableFile(path);
      const remote = remoteFromFile(file);
      if (remote.path !== path) return null;
      const latest = modelRef.current;
      // Typing or a save that started while the GET was in flight wins; the
      // next probe re-observes and reports the change as an advisory instead.
      if (!latest || latest.path !== path || savingRef.current || !isCleanForFollow(latest)) return null;
      if (remote.revision === latest.baseDiskRevision) {
        if (latest.externalObservation) setModel(observeExternal(latest, null));
        return null;
      }
      setModel(followExternal(latest, remote));
      return file;
    } catch (error) {
      const latest = modelRef.current;
      if (!latest || latest.path !== path || !isCleanForFollow(latest)) return null;
      const code = error instanceof Error ? error.message : "";
      // The full GET can disagree with the probe (§7.5): the file may have
      // become uneditable or vanished in between. Those become advisories;
      // transport failures stay silent and the next trigger retries.
      if (code === "not_file" || code === "http_404") {
        setModel(observeExternal(latest, { kind: "missing" }));
      } else if (
        ["binary", "invalid_utf8", "too_large", "unsupported_newline", "read_only_root", "not_editable"].includes(code)
      ) {
        setModel(observeExternal(latest, { kind: "uneditable", reason: code }));
      }
      return null;
    }
  }, [setModel]);

  // Route one probe observation into the model (docs/log/44 §7.3–§7.5). Advisory
  // by contract: apart from the clean auto-follow, the phase never moves.
  const applyProbeResult = useCallback(
    async (result: FileProbeResult): Promise<EditableFile | null> => {
      const current = modelRef.current;
      if (!current || savingRef.current) return null;
      switch (result.kind) {
        case "unavailable":
          return null;
        case "missing":
          setModel(observeExternal(current, { kind: "missing" }));
          return null;
        case "uneditable":
          setModel(observeExternal(current, { kind: "uneditable", reason: result.reason }));
          return null;
        case "boundary":
          setModel(observeExternal(current, { kind: "boundary" }));
          return null;
        case "revision": {
          if (result.revision === current.baseDiskRevision) {
            // The disk matches the buffer's base again (our own save, or an
            // external change that was reverted) — retire any stale advisory.
            if (current.externalObservation) setModel(observeExternal(current, null));
            return null;
          }
          if (isCleanForFollow(current)) return followExternalChange();
          setModel(
            observeExternal(current, {
              kind: result.revision === current.bufferRevision ? "same_as_buffer" : "changed",
              revision: result.revision,
            }),
          );
          return null;
        }
      }
    },
    [followExternalChange, setModel],
  );

  // The dirty-side "review the diff" action (「差分を確認」, docs/log/44 §7.3): the
  // user asks to see the external change, so fetch the body and open the ordinary
  // conflict UI. This explicit action — never the probe itself — is what may create
  // Conflict.
  const confirmExternalChange = useCallback(async (): Promise<void> => {
    const current = modelRef.current;
    if (!current || current.phase !== "dirty" || savingRef.current) return;
    const path = current.path;
    try {
      const remote = remoteFromFile(await getEditableFile(path));
      if (remote.path !== path) throw new Error("path mismatch");
      const latest = modelRef.current;
      if (!latest || latest.path !== path || latest.phase !== "dirty") return;
      if (remote.revision === latest.bufferRevision) {
        setModel(observeExternal(latest, { kind: "same_as_buffer", revision: remote.revision }));
        return;
      }
      if (remote.revision === latest.baseDiskRevision) {
        setModel(observeExternal(latest, null));
        return;
      }
      setModel(conflictFound(latest, remote));
    } catch (error) {
      const latest = modelRef.current;
      if (!latest || latest.path !== path || latest.phase !== "dirty") return;
      setModel(conflictUnavailable(latest, String(error)));
    }
  }, [setModel]);

  // --- AI edit suggestions (docs/log/44 §4 / Phase 4) ---

  const [suggesting, setSuggesting] = useState(false);
  const suggestReqRef = useRef<string | null>(null);

  /** Generates a suggestion from the selection plus an instruction. On success it lands
   *  in model.suggestion and null is returned; on failure a stable code for display is
   *  returned, never an exception, because a suggestion is advisory. A response is only
   *  accepted after re-validating identity (path/requestId) and the three-way revision
   *  match — if the user typed while it was generating, that is `suggestion_stale`. */
  const requestSuggestion = useCallback(
    async (instruction: string, range: EditRange): Promise<string | null> => {
      const current = modelRef.current;
      if (!current || savingRef.current) return "suggestion_invalid";
      const trimmed = instruction.trim();
      if (
        trimmed === "" ||
        new TextEncoder().encode(trimmed).byteLength > SUGGEST_MAX_INSTRUCTION_BYTES
      ) {
        return "instruction_invalid";
      }
      const { from, to } = range;
      if (!Number.isInteger(from) || !Number.isInteger(to) || from < 0 || to < from || to > current.content.length) {
        return "suggestion_invalid";
      }
      const windows = suggestWindows(current.content, range);
      if (!windows) return "selection_too_large";
      const path = current.path;
      const paneIdAtRequest = current.paneId;
      const sourceRevision = current.bufferRevision;
      const requestId = `suggest-${++suggestSeq}`;
      suggestReqRef.current = requestId;
      setSuggesting(true);
      try {
        const result = await suggestEdit({ path, instruction: trimmed, ...windows });
        // Drop silently a response overtaken by a newer request, a file switch or a reject.
        if (suggestReqRef.current !== requestId) return null;
        const latest = modelRef.current;
        if (!latest || latest.path !== path) return null;
        if (!result.ok) return result.code;
        const envelope: EditSuggestionEnvelope = {
          kind: "edit_suggestion",
          version: 1,
          paneId: paneIdAtRequest,
          filePath: path,
          requestId,
          sourceRevision,
          suggestion: {
            summary: result.summary,
            replacement: result.replacement,
            range,
            baseRevision: sourceRevision,
          },
        };
        const check = checkSuggestion(envelope, latest);
        if (!check.ok) return check.code;
        setModel(setSuggestion(latest, envelope));
        return null;
      } finally {
        if (suggestReqRef.current === requestId) {
          suggestReqRef.current = null;
          setSuggesting(false);
        }
      }
    },
    [setModel],
  );

  /** Abandons the in-flight request; its response is discarded if it still arrives. */
  const cancelSuggestion = useCallback(() => {
    suggestReqRef.current = null;
    setSuggesting(false);
  }, []);

  /** Applies a suggestion to the buffer (docs/log/44 §4.3: never issues a PUT). When
   *  applyToView is supplied and returns true, CodeMirror's ranged transaction (one undo
   *  step, already through the validator) -> onChange -> editBuffer advances the text, so
   *  all that is left here is retiring the suggestion. With no editing surface, the model
   *  applies it and content sync takes over. On failure a stable code (`suggestion_stale`
   *  and friends) is returned and the suggestion is kept. */
  const acceptSuggestion = useCallback(
    (applyToView?: (edit: { from: number; to: number; insert: string }) => boolean): string | null => {
      const current = modelRef.current;
      const envelope = current?.suggestion;
      if (!current || !envelope) return "suggestion_invalid";
      const check = checkSuggestion(envelope, current);
      if (!check.ok) return check.code;
      if (applyToView) {
        const { range } = envelope.suggestion;
        if (!applyToView({ from: range.from, to: range.to, insert: envelope.suggestion.replacement })) {
          return "suggestion_invalid";
        }
        setModel(setSuggestion(modelRef.current!, null));
        return null;
      }
      setModel(applySuggestion(current));
      return null;
    },
    [setModel],
  );

  /** Discards the suggestion. The buffer is left untouched (docs/log/44 §1.3). */
  const rejectSuggestion = useCallback(() => {
    const current = modelRef.current;
    if (current?.suggestion) setModel(setSuggestion(current, null));
  }, [setModel]);

  const manualMerge = useCallback(() => {
    const current = modelRef.current;
    if (current?.conflict) {
      recoveryRef.current?.invalidate();
      mergeMineRef.current = current.content;
      setModel(startManualMerge(current));
    }
  }, [setModel]);

  return {
    model,
    mergeMine: mergeMineRef.current,
    edit,
    save,
    recoverConflict,
    recoverUnknown,
    resaveUnknown,
    riskAccept,
    takeRemote,
    discardRemote: takeRemote,
    manualMerge,
    discard,
    applyProbeResult,
    confirmExternalChange,
    suggesting,
    requestSuggestion,
    cancelSuggestion,
    acceptSuggestion,
    rejectSuggestion,
  };
}
