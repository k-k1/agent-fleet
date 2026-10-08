import { useEffect, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { QueuedSend } from "./queue.ts";

/**
 * SendQueueList draws the follow-ups held before sending: edit, reorder, delete, send now.
 * Nothing here is in the transcript yet — the rows are drafts, which is why they sit in the
 * composer and not among the turns.
 */
export function SendQueueList({
  items,
  paused,
  injects,
  onEdit,
  onRemove,
  onMove,
  onSendNow,
  onResume,
  onEditing,
}: {
  items: QueuedSend[];
  paused: boolean;
  /** A send during a running turn goes into that turn (codex/muse) — else it runs after it. */
  injects: boolean;
  onEdit: (id: string, text: string) => void;
  onRemove: (id: string) => void;
  onMove: (id: string, delta: -1 | 1) => void;
  onSendNow: (id: string) => void;
  onResume: () => void;
  /** The row now open for editing, or null. The drain stands still while one is open. */
  onEditing: (id: string | null) => void;
}) {
  const [edit, setEditing] = useState<{ id: string; text: string } | null>(null);
  // The list can disappear while the hook stays mounted (the composer is swapped out): release
  // this view's lock then. onEditing is bound to the view's own owner, so no other view is unlocked.
  useEffect(() => () => onEditing(null), []); // eslint-disable-line react-hooks/exhaustive-deps
  // An edit belongs to a row of THIS list: after a session switch or a deleted row it is void.
  const editing = edit && items.some((i) => i.id === edit.id) ? edit : null;
  if (!items.length) return null;
  const open = (id: string, text: string) => {
    onEditing(id);
    setEditing({ id, text });
  };
  const close = () => {
    setEditing(null);
    onEditing(null);
  };
  const commit = () => {
    if (editing) onEdit(editing.id, editing.text);
    close();
  };
  return (
    <div className="mirror-queue" role="group" aria-label={tr("mirror.queue_title", { n: items.length })}>
      <div className="mirror-queue-head">
        <span>{tr("mirror.queue_title", { n: items.length })}</span>
        {paused && (
          <span className="mirror-queue-paused">
            {tr("mirror.queue_paused")}{" "}
            <button type="button" className="ghost" onClick={onResume}>
              {tr("mirror.queue_resume")}
            </button>
          </span>
        )}
      </div>
      <ol className="mirror-queue-list">
        {items.map((it, i) => (
          <li key={it.id} className="mirror-queue-item">
            {editing?.id === it.id ? (
              <textarea
                className="mirror-queue-edit"
                autoFocus
                rows={2}
                value={editing.text}
                onChange={(e) => setEditing({ id: it.id, text: e.target.value })}
                onBlur={commit}
                onKeyDown={(e) => {
                  // Enter during an IME composition confirms the candidate, it must not save.
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return;
                  if (e.key === "Escape") {
                    e.preventDefault();
                    close();
                  } else if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    commit();
                  }
                }}
              />
            ) : (
              <span className="mirror-queue-text">
                {it.text || tr("mirror.queue_attach_only")}
                {it.paths.length > 0 && <span className="mirror-queue-files"> +{it.paths.length}</span>}
              </span>
            )}
            <span className="mirror-queue-acts">
              <button type="button" className="ghost" disabled={i === 0} title={tr("mirror.queue_up")} aria-label={tr("mirror.queue_up")} onClick={() => onMove(it.id, -1)}>
                <Icon name="arrow-up" />
              </button>
              <button type="button" className="ghost" disabled={i === items.length - 1} title={tr("mirror.queue_down")} aria-label={tr("mirror.queue_down")} onClick={() => onMove(it.id, 1)}>
                <Icon name="arrow-down" />
              </button>
              <button type="button" className="ghost" disabled={!it.text} title={tr("mirror.queue_edit")} aria-label={tr("mirror.queue_edit")} onClick={() => open(it.id, it.text)}>
                <Icon name="edit" />
              </button>
              <button
                type="button"
                className="ghost"
                title={tr(injects ? "mirror.queue_now_inject" : "mirror.queue_now_after")}
                aria-label={tr("mirror.queue_now")}
                onClick={() => onSendNow(it.id)}
              >
                <Icon name="send" />
              </button>
              <button type="button" className="ghost" title={tr("mirror.queue_remove")} aria-label={tr("mirror.queue_remove")} onClick={() => onRemove(it.id)}>
                <Icon name="trash" />
              </button>
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}
