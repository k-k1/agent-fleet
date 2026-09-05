// DocView — in-memory Markdown (e.g. a plan) in its own pane, no file on disk.
// The content lives in the pane descriptor. Port of views/DocView.
//
// Opened as a plan (with a docSession) it becomes a review surface: selecting text offers a pill
// to add a comment, and each comment highlights the passage it quotes. Comments only accumulate
// here; sending is the mirror's plan card, because the route depends on the target's state
// (awaiting approval vs already rejected) and that decision belongs to whoever holds the session
// state.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { createPortal } from "react-dom";
import { MarkdownView } from "./MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { SelectionFloat } from "../../ui/SelectionFloat.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useSelectionCapture } from "../../lib/selectionCapture.ts";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { applyQuoteMarks, selectionAnchor } from "./quoteMarks.ts";
import {
  addPlanComment,
  MAX_COMMENTS,
  planKey,
  removePlanComment,
  usePlanComments,
} from "../mirror/planComments.ts";

interface DocViewProps {
  title?: string;
  content?: string;
  /** Session this was opened from; set only for a plan. Its presence enables the review surface. */
  session?: string;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

interface PendingComment {
  quote: string;
  nth: number;
  x: number;
  y: number;
}

/** Lift that places the selection pill just above the selection (same value as FileView). */
const PILL_OFFSET = 34;
/** Minimum margin from the edge of the viewport. */
const EDGE = 8;
/** How many pending comments the list shows at once; the rest scroll. */
const VISIBLE_COMMENTS = 5;

// clampFixed pulls a position:fixed floating element back inside the viewport using its measured
// size. Using the selection position directly as left/top pushes the popup off screen for a
// selection near the right or bottom edge and the buttons become unreachable (measured: selecting
// a line at the right edge cuts off the add button). The measurement has to happen after paint,
// so the style is written directly rather than through state, which would loop the render.
function clampFixed(el: HTMLElement | null, x: number, y: number) {
  if (!el) return;
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  const maxX = Math.max(EDGE, window.innerWidth - w - EDGE);
  const maxY = Math.max(EDGE, window.innerHeight - h - EDGE);
  el.style.left = Math.round(Math.min(Math.max(EDGE, x), maxX)) + "px";
  el.style.top = Math.round(Math.min(Math.max(EDGE, y), maxY)) + "px";
}

export function DocView({ title, content, session, headerActions }: DocViewProps) {
  const tr = useT();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
  const settings = useSettings();
  const bodyRef = useRef<HTMLDivElement>(null);
  const key = session ? planKey(session, content || "") : null;
  const comments = usePlanComments(key);
  // The pill attached to a selection (null = hidden), and the comment input opened from it.
  const [pill, setPill] = useState<PendingComment | null>(null);
  const [draft, setDraft] = useState<PendingComment | null>(null);
  const [body, setBody] = useState("");
  const [listOpen, setListOpen] = useState(true);
  // Comments expanded to full text (neither quote nor body clipped). The list is collapsed by
  // default.
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const popRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLOListElement>(null);
  // Follows the composer's send-key setting: Ctrl+Enter or Enter (lib/settings mirrorSend).
  const modSend = settings.mirrorSend !== "enter";

  // The pending list is cut off at the height of five rows. Row height varies with comment length
  // (one quote line plus up to two body lines), so a fixed CSS value would not mean "five" —
  // measure the sixth row's position instead. Cap at 40% of the viewport so an expanded row
  // cannot stretch it too far.
  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el) return;
    const rows = [...el.querySelectorAll<HTMLElement>(".doc-comment")];
    if (rows.length <= VISIBLE_COMMENTS) {
      el.style.maxHeight = "";
      return;
    }
    const top = rows[0].getBoundingClientRect().top;
    const fit = Math.round(rows[VISIBLE_COMMENTS].getBoundingClientRect().top - top);
    el.style.maxHeight = Math.min(fit, Math.round(window.innerHeight * 0.4)) + "px";
  }, [comments, listOpen, expanded]);

  // Floating elements are measured after paint and then clamped, and re-clamped on resize while
  // open, so splitting a pane or rotating a phone cannot leave the buttons unreachable. (The
  // pill does the same through SelectionFloat, which also decides floating vs docked.)
  useLayoutEffect(() => {
    if (!draft) return;
    const fit = () => clampFixed(popRef.current, draft.x, draft.y);
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [draft]);

  // Quote highlights are laid over the body after MarkdownView has written its innerHTML: child
  // effects run first, so the body exists by the time this effect runs. On a re-render where
  // neither the body nor the comments changed, MarkdownView does nothing and the marks survive.
  useEffect(() => {
    const root = bodyRef.current?.querySelector<HTMLElement>(".markdown");
    if (!root) return;
    applyQuoteMarks(root, key ? comments.map((c) => ({ quote: c.quote, nth: c.nth })) : []);
  }, [content, comments, key]);

  // Show the pill once a selection settles (lib/selectionCapture — shared with ReaderView,
  // FileView and the transcript marks).
  const capture = () => {
    const root = bodyRef.current?.querySelector<HTMLElement>(".markdown");
    if (!key || !root) return;
    const anchor = selectionAnchor(root);
    if (!anchor) {
      setPill(null);
      return;
    }
    setPill({
      quote: anchor.quote,
      nth: anchor.nth,
      x: Math.round(anchor.rect.left),
      y: Math.round(anchor.rect.top - PILL_OFFSET),
    });
  };
  useSelectionCapture(capture);

  const startDraft = () => {
    if (!pill) return;
    setDraft(pill);
    setBody("");
    setPill(null);
  };
  const closeDraft = () => {
    setDraft(null);
    setBody("");
  };
  const submitDraft = () => {
    if (!key || !draft || !body.trim()) return;
    addPlanComment(key, { quote: draft.quote, nth: draft.nth, body });
    closeDraft();
    window.getSelection()?.removeAllRanges();
  };

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
  } as CSSProperties;
  const full = key ? comments.length >= MAX_COMMENTS : false;
  return (
    <div className="fileview docview" style={viewerStyle}>
      <ViewHead className="fileinfo" actions={headerActions}>
        <span className="fi-name mono">
          <Icon name="checklist" /> {title || tr("view.document")}
        </span>
        <span className="fi-tag">Markdown</span>
        {key && (
          <span className="fi-tag doc-review-tag" title={tr("plan.review_hint")}>
            <Icon name="comment-discussion" /> {tr("plan.review_tag")}
          </span>
        )}
      </ViewHead>
      <div className="md-scroll" ref={bodyRef} onMouseUp={() => capture()}>
        <MarkdownView
          source={content || ""}
          onOpenFile={(path, line, column, openInNew) => {
            const target = { content: { kind: "file" as const, filePath: path, targetLine: line, targetColumn: column } };
            if (openInNew) openTargetInNew(target, true);
            else openTarget(target);
          }}
          // The click navigates there, so the file tree takes keyboard focus along with it.
          onOpenDir={(path) => revealInFiles(path, { focus: true })}
        />
      </div>

      {key && comments.length > 0 && (
        // The pending comments. Sending happens in the mirror, so this offers only review and
        // deletion.
        <div className={"doc-comments" + (listOpen ? "" : " collapsed")}>
          <button type="button" className="doc-comments-head" onClick={() => setListOpen((v) => !v)}>
            <Icon name={listOpen ? "chevron-down" : "chevron-up"} />
            <span>{tr("plan.comments_count", { count: comments.length })}</span>
            <span className="muted doc-comments-hint">{tr("plan.send_from_mirror")}</span>
          </button>
          {listOpen && (
            // Cut off at five rows and scroll, so the list stays out of the body's way. Collapsed
            // rows are all the same height, so that count matches what is actually seen.
            <ol className="doc-comments-list" ref={listRef}>
              {comments.map((c, i) => {
                const open = expanded.has(c.id);
                return (
                  <li key={c.id} className={"doc-comment" + (c.sentAt ? " sent" : "") + (open ? " open" : "")}>
                    <span className="doc-comment-n">{i + 1}</span>
                    <button
                      type="button"
                      className="doc-comment-main"
                      title={open ? tr("plan.collapse_comment") : tr("plan.expand_comment")}
                      onClick={() =>
                        setExpanded((prev) => {
                          const next = new Set(prev);
                          next.has(c.id) ? next.delete(c.id) : next.add(c.id);
                          return next;
                        })
                      }
                    >
                      <span className="doc-comment-quote">{c.quote}</span>
                      <span className="doc-comment-body">{c.body}</span>
                    </button>
                    {c.sentAt ? (
                      <span className="doc-comment-sent muted">{tr("plan.sent")}</span>
                    ) : (
                      <button
                        type="button"
                        className="ghost doc-comment-del"
                        title={tr("plan.delete_comment")}
                        onClick={() => removePlanComment(key, c.id)}
                      >
                        <Icon name="close" />
                      </button>
                    )}
                  </li>
                );
              })}
            </ol>
          )}
        </div>
      )}

      {/* Portalled to body: .fileview sets container-type, so a position:fixed element inside it
          is positioned against the pane rather than the viewport (same reason as FileView's
          selection pill). */}
      {pill &&
        !draft &&
        createPortal(
          <SelectionFloat x={pill.x} y={pill.y} className="sel-pill-group">
            <button
              type="button"
              className="sel-send-pill"
              onMouseDown={(e) => e.preventDefault()} // keeps the click from clearing the selection
              onClick={startDraft}
              disabled={full}
              title={full ? tr("plan.comments_full") : undefined}
            >
              <Icon name="comment-discussion" /> {tr("plan.add_comment")}
            </button>
          </SelectionFloat>,
          document.body,
        )}
      {draft &&
        createPortal(
          <div className="doc-comment-pop" ref={popRef} style={{ left: draft.x, top: Math.max(EDGE, draft.y) }}>
            <div className="doc-comment-pop-quote">{draft.quote}</div>
            <textarea
              className="doc-comment-pop-input"
              autoFocus
              rows={3}
              value={body}
              placeholder={tr(modSend ? "plan.comment_placeholder" : "plan.comment_placeholder_enter")}
              onChange={(e) => setBody(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  closeDraft();
                  return;
                }
                // The send key follows the composer's setting. Never intercept the Enter that
                // commits an IME composition.
                if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
                const mod = e.ctrlKey || e.metaKey;
                if (modSend ? mod : !mod && !e.shiftKey) {
                  e.preventDefault();
                  submitDraft();
                }
              }}
            />
            <div className="doc-comment-pop-actions">
              <button type="button" className="ghost" onClick={closeDraft}>
                {tr("common.cancel")}
              </button>
              <button type="button" className="btn primary" disabled={!body.trim()} onClick={submitDraft}>
                {tr("plan.add_comment")}
              </button>
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}
