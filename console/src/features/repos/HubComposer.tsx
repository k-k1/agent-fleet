// HubComposer — the Start hub's prompt box (ADR 0142 decision 5). Every section of the hub shows
// this same box over the one shared draft, so switching sections keeps the text; it is mounted
// per section and takes the focus on mount, which is what moves the focus into the section just
// opened.
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { ClipboardEvent, DragEvent, KeyboardEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { makeAttachment } from "../../lib/attachDraft.ts";
import { AttachChips } from "../mirror/parts/AttachChips.tsx";
import { ImageLightbox } from "../viewer/ImageLightbox.tsx";
import { useBackClose } from "../../lib/backClose.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useSettings } from "../../lib/settings.ts";
import type { SharedDraft } from "./hubDraft.ts";

interface HubComposerProps {
  draft: SharedDraft;
  busy: boolean;
  /** What this section does with the draft, under the box. */
  note?: string;
  /** The send key (per the composer setting) launches this section's start, when it has one. */
  onSubmit?: () => void;
}

export function HubComposer({ draft, busy, note, onSubmit }: HubComposerProps) {
  const tr = useT();
  const settings = useSettings();
  const textRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  // Never on a touch device: focus opens the soft keyboard over most of the dialog.
  useEffect(() => {
    if (!coarsePointer()) textRef.current?.focus({ preventScroll: true });
  }, []);
  const [zoom, setZoom] = useState<string | null>(null);
  useBackClose(zoom ? () => setZoom(null) : undefined, !!zoom);

  const locked = busy || draft.loading;
  const addFiles = (files: File[]) => {
    if (locked || !files.length) return;
    draft.attach.add(files.map((f) => makeAttachment(f)));
  };
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (let i = 0; i < items.length; i++) {
      const f = items[i].kind === "file" ? items[i].getAsFile() : null;
      if (f) files.push(f);
    }
    if (!files.length) return; // a text paste goes in as usual
    e.preventDefault();
    addFiles(files);
  };
  const dropsFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes("Files");
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!onSubmit || e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.metaKey || e.ctrlKey;
    if (settings.mirrorSend !== "enter" ? mod : !e.shiftKey && !mod) {
      e.preventDefault();
      onSubmit();
    }
  };

  return (
    <div className="ui-field start-draft">
      <span className="ui-field-label launch-prompt-label">
        <label htmlFor="start-draft-text">{tr("start.draft_label")}</label>
        <span className="launch-prompt-tools">
          <input
            ref={fileRef}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              const files = Array.from(e.target.files || []);
              e.target.value = ""; // the same file can be picked again
              addFiles(files);
            }}
          />
          <button type="button" className="ghost launch-attach-btn" title={tr("launch.attach_file")} aria-label={tr("launch.attach_file")} disabled={locked} onClick={() => fileRef.current?.click()}>
            <Icon name="add" />
          </button>
        </span>
      </span>
      {draft.missing > 0 && (
        <div className="launch-stash-note launch-stash-note--warn" role="status">
          {tr("launch.memo_attach_missing", { count: draft.missing })}
        </div>
      )}
      <AttachChips attachments={draft.attach.items} pasting={draft.loading} onRemove={(i) => !locked && draft.attach.remove(i)} onOpen={setZoom} />
      {zoom && createPortal(<ImageLightbox src={zoom} className="over-modal" onClose={() => setZoom(null)} />, document.body)}
      <textarea
        id="start-draft-text"
        ref={textRef}
        value={draft.prompt}
        readOnly={busy}
        onChange={(e) => draft.setPrompt(e.target.value)}
        onKeyDown={onKey}
        onPaste={onPaste}
        onDragOver={(e) => dropsFiles(e) && e.preventDefault()}
        onDrop={(e) => {
          if (!dropsFiles(e)) return;
          e.preventDefault();
          addFiles(Array.from(e.dataTransfer.files || []));
        }}
        rows={3}
        placeholder={tr("start.draft_ph")}
      />
      {note && <span className="ui-field-hint">{note}</span>}
    </div>
  );
}
