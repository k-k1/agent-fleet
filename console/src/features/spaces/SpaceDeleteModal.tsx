// SpaceDeleteModal — delete a space through the trash (ADR 0142 decision 2). The folder and its
// files move to the trash and its sessions go with it, the way deleting a working copy does: AI
// sessions to the shelf, shell and SSM sessions to the trash, and Cleanup → Trash puts all of it
// back. A session that is running or locked is refused by the Agent whatever is sent, so the
// dialog says which and leaves the button off rather than offering a press that cannot work.
import { useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { isTerminalSession } from "../../lib/project.ts";
import { displayName } from "../../lib/sessionview.ts";
import { deleteSpace, spacesOwner } from "./store.ts";

// The Agent's refusals, worded here: the shared codes say "working copy".
const DELETE_ERRORS = {
  space_not_found: "pj.space_delete_gone",
  space_sessions_running: "pj.space_delete_running_error",
  space_locked_sessions: "pj.space_delete_locked_error",
  space_sessions_trash_failed: "pj.space_delete_sessions_trash_failed",
  space_trash_failed: "pj.space_delete_trash_failed",
  space_trash_other_filesystem: "pj.space_delete_other_fs",
  delete_in_progress: "pj.space_delete_in_progress",
} as const;

/** `owner` is the workspace the dialog was opened in; the delete is only sent to it, and the
 *  dialog is closed by its parent when the workspace changes. */
export function SpaceDeleteModal({
  folder,
  label,
  owner,
  onClose,
}: {
  folder: string;
  label: string;
  owner: string;
  onClose: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  // Frozen at open: the list is polled, and a row changing under the pointer is how a press lands
  // on something the member never read.
  const [mine] = useState(() => useSessionsStore.getState().sessions.filter((s) => s.space === folder));
  const alive = mine.filter((s) => s.alive);
  const locked = mine.filter((s) => s.locked);
  const shells = mine.filter((s) => isTerminalSession(s)).length;
  const ai = mine.length - shells;
  const blocked = alive.length > 0 || locked.length > 0;
  const [busy, setBusy] = useState(false);

  const names = (list: typeof mine) => list.map((s) => displayName(s)).join(", ");

  const run = async () => {
    if (busy || blocked) return;
    setBusy(true);
    try {
      const res = await deleteSpace(folder, owner);
      if (res?.stale) {
        onClose();
        return;
      }
      if (res?.error) {
        const err = res.error as Parameters<typeof errText>[0];
        const code = err && typeof err === "object" && err.code ? (err.code as keyof typeof DELETE_ERRORS) : undefined;
        const key = code ? DELETE_ERRORS[code] : undefined;
        toast(key ? tr(key) : tr("pj.space_delete_error", { detail: errText(err) }));
        return;
      }
      for (const s of mine) closeSessionPanes(s.name);
      // The shelved sessions leave the list on the next poll; ask now so the row does not linger.
      void useSessionsStore.getState().refresh();
      toast(tr("pj.space_deleted", { name: label }));
      onClose();
    } catch {
      if (spacesOwner() === owner) toast(tr("pj.space_delete_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("pj.space_delete_title")} onClose={onClose} lockClose={busy}>
      <div className="ui-modal-body">
        <p>{tr("pj.space_delete_body", { name: label, folder })}</p>
        {mine.length > 0 && <p>{tr("pj.space_delete_sessions", { ai, shells })}</p>}
        <p className="ui-field-hint">{tr("pj.space_delete_restore")}</p>
        {alive.length > 0 && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_running", { names: names(alive) })}
          </p>
        )}
        {locked.length > 0 && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_locked", { names: names(locked) })}
          </p>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="danger" onClick={() => void run()} disabled={busy || blocked} data-autofocus>
          {tr("pj.space_delete_do")}
        </Button>
      </footer>
    </Modal>
  );
}
