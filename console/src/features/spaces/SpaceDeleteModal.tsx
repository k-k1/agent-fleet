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
import type { Session } from "../../types/session.ts";
import { isTerminalSession } from "../../lib/project.ts";
import { sessionInSpace } from "./running.ts";
import { displayName } from "../../lib/sessionview.ts";
import { deleteSpace, spacesOwner, useSpacesStore } from "./store.ts";

// The Agent's refusals, worded here: the shared codes say "working copy".
const DELETE_ERRORS = {
  space_not_found: "pj.space_delete_gone",
  space_sessions_running: "pj.space_delete_running_error",
  space_locked_sessions: "pj.space_delete_locked_error",
  space_sessions_trash_failed: "pj.space_delete_sessions_trash_failed",
  space_sessions_unobservable: "pj.space_delete_unobservable",
  space_sessions_changed: "pj.space_delete_changed_agent",
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
  // The Agent settles every session at or under the space's folder, so one started in a
  // subfolder counts as well as one named by `space`.
  const dir = useSpacesStore(
    (s) => s.spaces.find((x) => x.folder === folder)?.dir ?? "",
  );
  const all = useSessionsStore((s) => s.sessions);
  // Sessions the Agent found in the space that the Console's own rule did not (a path through a
  // link it cannot resolve): named by a space_sessions_changed refusal, and shown from then on.
  const [extra, setExtra] = useState<ReadonlySet<string>>(() => new Set());
  const inSpace = (s: Session) => extra.has(s.name) || sessionInSpace(s, folder, dir);
  const current = all.filter(inSpace);
  // What the member read: frozen, so a press never lands on a set they did not see. When the
  // sessions in the space change while the dialog is open (a shell opened elsewhere, one removed)
  // the button waits for a second look rather than deleting what was never listed.
  const [reviewed, setReviewed] = useState<Session[]>(() =>
    useSessionsStore.getState().sessions.filter(inSpace),
  );
  const currentNames = current
    .map((s) => s.name)
    .sort()
    .join("\n");
  const changed =
    currentNames !==
    reviewed
      .map((s) => s.name)
      .sort()
      .join("\n");
  // Whether a reviewed session runs or is locked is read live: stopping or unlocking one
  // elsewhere is the way to unblock the button.
  const live = new Map(all.map((s) => [s.name, s]));
  const mine = reviewed.map((s) => live.get(s.name) ?? s);
  const alive = mine.filter((s) => s.alive);
  const locked = mine.filter((s) => s.locked);
  const shells = reviewed.filter((s) => isTerminalSession(s)).length;
  const ai = reviewed.length - shells;
  // Without the space's location a session started in a subfolder cannot be told from one that is
  // elsewhere, so the list read above may be short of what the Agent will settle: no press, and a
  // way to ask again, until the list names it.
  const noDir = !dir;
  const blocked = alive.length > 0 || locked.length > 0 || changed || noDir;
  const [busy, setBusy] = useState(false);

  const listed = (list: typeof mine) =>
    list.map((s) => displayName(s)).join(", ");

  const run = async () => {
    if (busy || blocked) return;
    setBusy(true);
    try {
      const res = await deleteSpace(
        folder,
        owner,
        reviewed.map((s) => s.name),
      );
      if (res?.stale) {
        onClose();
        return;
      }
      if (res?.error) {
        const err = res.error as Parameters<typeof errText>[0];
        const code =
          err && typeof err === "object" && err.code
            ? (err.code as keyof typeof DELETE_ERRORS)
            : undefined;
        const key = code ? DELETE_ERRORS[code] : undefined;
        if (code === "space_sessions_changed") {
          const named = (err as { sessions?: unknown }).sessions;
          if (Array.isArray(named)) {
            setExtra((prev) => new Set([...prev, ...named.filter((n): n is string => typeof n === "string")]));
          }
        }
        // What could not be observed (which record, what tmux said) is the only clue when it does
        // not pass on its own, so it is shown after the wording.
        const detail =
          code === "space_sessions_unobservable" && typeof err === "object" && err?.message
            ? " " + err.message
            : "";
        toast(
          key ? tr(key) + detail : tr("pj.space_delete_error", { detail: errText(err) }),
        );
        return;
      }
      for (const s of reviewed) closeSessionPanes(s.name);
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
    <Modal
      title={tr("pj.space_delete_title")}
      onClose={onClose}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <p>{tr("pj.space_delete_body", { name: label, folder })}</p>
        {reviewed.length > 0 && (
          <p>{tr("pj.space_delete_sessions", { ai, shells })}</p>
        )}
        {noDir && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_no_dir")}{" "}
            <Button variant="ghost" small onClick={() => void useSpacesStore.getState().refresh()}>
              {tr("pj.space_delete_reload")}
            </Button>
          </p>
        )}
        {changed && !noDir && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_changed")}{" "}
            <Button variant="ghost" small onClick={() => setReviewed(current)}>
              {tr("pj.space_delete_review")}
            </Button>
          </p>
        )}
        <p className="ui-field-hint">{tr("pj.space_delete_restore")}</p>
        {alive.length > 0 && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_running", { names: listed(alive) })}
          </p>
        )}
        {locked.length > 0 && (
          <p role="alert" className="ui-field-hint">
            {tr("pj.space_delete_locked", { names: listed(locked) })}
          </p>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("sx.cancel")}
        </Button>
        <Button
          variant="danger"
          onClick={() => void run()}
          disabled={busy || blocked}
          data-autofocus
        >
          {tr("pj.space_delete_do")}
        </Button>
      </footer>
    </Modal>
  );
}
