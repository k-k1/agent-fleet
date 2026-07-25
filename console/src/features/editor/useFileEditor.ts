import { useCallback, useEffect, useRef, useState } from "react";
import { getEditableFile, putFile } from "./api.ts";
import {
  acceptUnknownRisk,
  adoptRemote,
  beginSave,
  classifyUnknownRemote,
  conflictFound,
  conflictUnavailable,
  createFileEditorModel,
  editBuffer,
  observeUnknown,
  prepareUnknownResave,
  saveFailed,
  saveStateUnknown,
  saveSucceeded,
  startManualMerge,
  type FileEditorModel,
  type RemoteSnapshot,
  type SaveSnapshot,
} from "./model.ts";
import { revisionOf } from "./buffer.ts";
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

function remoteFromFile(file: Awaited<ReturnType<typeof getEditableFile>>): RemoteSnapshot {
  if (revisionOf(file.content) !== file.revision) throw new Error("revision/content mismatch");
  return { path: file.path, content: file.content, revision: file.revision };
}

export function useFileEditor(paneId: string, initial: InitialEditableFile | null) {
  const [model, setReactModel] = useState<FileEditorModel | null>(() =>
    initial ? createFileEditorModel(paneId, initial.path, initial.content, initial.revision) : null,
  );
  const modelRef = useRef(model);
  const savingRef = useRef<Promise<boolean> | null>(null);
  const mergeMineRef = useRef<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
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
      modelRef.current = null;
      setReactModel(null);
      return;
    }
    const current = modelRef.current;
    if (current?.path === initial.path) return;
    const next = createFileEditorModel(paneId, initial.path, initial.content, initial.revision);
    modelRef.current = next;
    setReactModel(next);
  }, [initial, paneId]);

  const recoverConflict = useCallback(async () => {
    const current = modelRef.current;
    if (!current) return;
    try {
      const remote = remoteFromFile(await getEditableFile(current.path));
      if (remote.path !== current.path) throw new Error("path mismatch");
      setModel(conflictFound(modelRef.current!, remote));
    } catch (error) {
      setModel(conflictUnavailable(modelRef.current!, String(error)));
    }
  }, [setModel]);

  const recoverUnknown = useCallback(async (snapshot?: SaveSnapshot) => {
    const current = modelRef.current;
    const sent = snapshot || current?.saveSnapshot;
    if (!current || !sent) return;
    try {
      const remote = remoteFromFile(await getEditableFile(current.path));
      if (remote.path !== current.path) throw new Error("path mismatch");
      setModel(observeUnknown(modelRef.current!, classifyUnknownRemote(sent, remote)));
    } catch (error) {
      setModel(observeUnknown(modelRef.current!, { kind: "unavailable", message: String(error) }));
    }
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

  const discard = useCallback(() => {
    const current = modelRef.current;
    if (!current) return;
    setModel({ ...current, dirty: false, phase: "clean", saveSnapshot: null, conflict: null });
  }, [setModel]);

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
    if (current) setModel(acceptUnknownRisk(current));
  }, [setModel]);

  const takeRemote = useCallback(() => {
    const current = modelRef.current;
    if (current?.conflict) {
      mergeMineRef.current = null;
      setModel(adoptRemote(current));
    }
  }, [setModel]);

  const manualMerge = useCallback(() => {
    const current = modelRef.current;
    if (current?.conflict) {
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
  };
}
