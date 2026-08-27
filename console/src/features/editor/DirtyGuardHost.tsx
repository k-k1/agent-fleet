import { useEffect, useSyncExternalStore, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import {
  cancelDirtyGuardRequest,
  currentDirtyGuardRequest,
  discardDirtyGuardRequest,
  hasDirtyEditors,
  saveDirtyGuardRequest,
  subscribeDirtyRegistry,
} from "./dirtyRegistry.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

const REASON: Record<string, MsgKey> = {
  layout: "editor.guard.layout",
  history: "editor.guard.history",
  tenant: "editor.guard.tenant",
  popout: "editor.guard.popout",
  reload: "editor.guard.reload",
  logout: "editor.guard.logout",
  version_update: "editor.guard.version_update",
  workspace_lifecycle: "editor.guard.workspace_lifecycle",
};

export function DirtyGuardHost() {
  const tr = useT();
  const current = useSyncExternalStore(
    subscribeDirtyRegistry,
    currentDirtyGuardRequest,
    () => null,
  );
  const [saving, setSaving] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);

  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!hasDirtyEditors()) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, []);

  useEffect(() => {
    if (!current) return;
    // This dialog deliberately has no useBackClose history sentinel: consuming
    // such a sentinel after a layout decision would emit popstate and restore
    // the just-discarded file layout. Back instead cancels the pending decision;
    // layout history's own listener restores the current entry. Cancelling is
    // safe even while a save/discard is processing: the request's AbortSignal
    // stops a pending discard before it cleans the buffer.
    const onPop = () => cancelDirtyGuardRequest(current.id);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, [current]);

  useEffect(() => {
    setSaving(false);
    setSaveFailed(false);
  }, [current?.id]);

  if (!current) return null;
  // A save/discard can stall on the network for its full I/O timeout; cancel
  // and close stay enabled the whole time as the abort-propagating escape
  // hatch. Only the save/discard buttons lock against double submission.
  const settle = (ok: boolean) => {
    if (ok || currentDirtyGuardRequest()?.id !== current.id) return;
    setSaving(false);
    setSaveFailed(true);
  };
  return (
    <Modal
      title={tr("editor.guard.title")}
      onClose={() => cancelDirtyGuardRequest(current.id)}
      backClose={false}
      className="dirty-guard-modal"
    >
      <div className="ui-modal-body">
        <p>{tr(REASON[current.reason] || REASON.layout)}</p>
        <ul>
          {current.entries.map((entry) => <li key={entry.paneId}>{entry.label}</li>)}
        </ul>
        {saveFailed && (
          <p role="alert">{tr("editor.guard.save_failed")}</p>
        )}
      </div>
      {/* ui-modal-foot が余白・区切り線・右寄せを持つ。ui-modal-actions は
          どの CSS にも無いクラスで、この footer は素のままだった。 */}
      <footer className="ui-modal-foot">
        <button
          type="button"
          className="primary"
          disabled={saving}
          autoFocus
          onClick={() => {
            setSaving(true);
            setSaveFailed(false);
            void saveDirtyGuardRequest(current.id).then(settle);
          }}
        >
          {tr("editor.guard.save")}
        </button>
        <button
          type="button"
          disabled={saving}
          onClick={() => {
            setSaving(true);
            setSaveFailed(false);
            void discardDirtyGuardRequest(current.id).then(settle);
          }}
        >
          {tr("editor.guard.discard")}
        </button>
        <button type="button" onClick={() => cancelDirtyGuardRequest(current.id)}>
          {tr("editor.cancel")}
        </button>
      </footer>
    </Modal>
  );
}
