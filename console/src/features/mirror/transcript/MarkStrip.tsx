// transcript/MarkStrip — the list of marks drawn on a conversation (docs/log/69 §69.7).
//
// This is the primary route to "who drew this": the `<mark>` in the body only encodes the author
// in its underline colour, never a name, so as not to disturb reading. The fold behaviour, the
// per-session open/closed memory and hiding the whole strip when empty match the changed-files
// strip (FileChangeStrip / docs/log/68) — two panels that fold differently side by side read as
// two unrelated features.
//
// Clicking a row scrolls to that mark. The transcript only holds a tail window, though, so a
// mark on a turn that has not been loaded is not on screen at all; those rows render disabled,
// which looks less broken than a row that does nothing when clicked.

import { useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { DisclosureContent, readLS, writeLS } from "./blocks.tsx";
import { MARK_CLASS } from "./markPaint.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";

function scrollToMark(id: string): boolean {
  const el = document.querySelector<HTMLElement>(`mark.${MARK_CLASS}[data-mark-id="${CSS.escape(id)}"]`);
  if (!el) return false;
  el.scrollIntoView({ block: "center", behavior: "smooth" });
  return true;
}

export function MarkStrip({ marks, storageKey }: { marks: TranscriptMarksWiring; storageKey: string }) {
  const tr = useT();
  const openKey = "af.mirror-marks-open." + storageKey;
  const [open, setOpen] = useState(() => readLS(openKey) === "1");

  // Never render an empty strip: a "0" cannot tell "nobody has drawn one yet" apart from "marks
  // are not available on this surface", and it takes up room permanently.
  if (!marks.all.length) return null;

  const authors = new Set(marks.all.map((m) => m.author || ""));

  return (
    <section className={"mirror-marks mirror-disclosure" + (open ? " open" : "")}>
      <div className="mirror-marks-head">
        <button
          type="button"
          className="mirror-marks-toggle"
          aria-expanded={open}
          onClick={() => {
            const next = !open;
            setOpen(next);
            writeLS(openKey, next ? "1" : "0");
          }}
        >
          <Icon name="paintcan" />
          <span className="mfl-title">{tr("mirror.mark.strip_title")}</span>
          <span className="mfl-count muted">{marks.all.length}</span>
          {/* Only when two or more people have drawn: show who is involved even while folded. */}
          {authors.size > 1 && (
            <span className="mfl-lead muted">{tr("mirror.mark.strip_authors", { n: authors.size })}</span>
          )}
        </button>
      </div>
      <DisclosureContent open={open} className="mirror-marks-list-wrap">
        <ul className="mirror-marks-list">
          {marks.all.map((m) => (
            <li key={m.id} className="tmark-row">
              <button
                type="button"
                className="tmark-row-btn"
                title={m.quote}
                onClick={(e) => {
                  if (!scrollToMark(m.id)) (e.currentTarget as HTMLButtonElement).disabled = true;
                }}
              >
                <span className={"tmark-chip tmark-" + m.color} />
                <span className="tmark-row-quote">{m.quote}</span>
                <span className="tmark-row-who muted">
                  <span className={"tmark-dot tmark-a" + marks.authorSlot(m.author)} />
                  {marks.authorLabel(m.author)}
                </span>
              </button>
              {marks.canRemove(m) && (
                <button
                  type="button"
                  className="tmark-row-del"
                  title={tr("mirror.mark.remove")}
                  aria-label={tr("mirror.mark.remove")}
                  onClick={() => marks.remove(m.id)}
                >
                  <Icon name="trash" />
                </button>
              )}
            </li>
          ))}
        </ul>
      </DisclosureContent>
    </section>
  );
}
