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
  discardToBase,
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

  const discard = useCallback(() => {
    const current = modelRef.current;
    if (!current) return;
    recoveryRef.current?.invalidate();
    mergeMineRef.current = null;
    setModel(discardToBase(current));
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
  };
}
