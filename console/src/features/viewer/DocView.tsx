// DocView — in-memory Markdown (e.g. a plan) in its own pane, no file on disk.
// The content lives in the pane descriptor. Port of views/DocView.
//
// プランとして開かれた（docSession を持つ）ときは「レビュー面」になる: 本文を選択すると
// コメント追加のピルが出て、付けたコメントは引用箇所にハイライトが付く。溜めるだけで、
// 送信はミラーのプランカードが担う（送り先の状態＝承認待ちか却下後かで経路が変わるため、
// 判断はセッションの状態を持っている側に置く）。
import { useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { createPortal } from "react-dom";
import { MarkdownView } from "./MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
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
  /** どのセッションから開かれたか（プランのときだけ入る）。レビュー面の有効化条件。 */
  session?: string;
}

interface PendingComment {
  quote: string;
  nth: number;
  x: number;
  y: number;
}

/** 選択ピルを選択の少し上に出すための持ち上げ量（FileView と同じ値）。 */
const PILL_OFFSET = 34;

export function DocView({ title, content, session }: DocViewProps) {
  const tr = useT();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
  const settings = useSettings();
  const bodyRef = useRef<HTMLDivElement>(null);
  const key = session ? planKey(session, content || "") : null;
  const comments = usePlanComments(key);
  // 選択に添えるピル（null = 出さない）と、ピルから開いたコメント入力。
  const [pill, setPill] = useState<PendingComment | null>(null);
  const [draft, setDraft] = useState<PendingComment | null>(null);
  const [body, setBody] = useState("");
  const [listOpen, setListOpen] = useState(true);

  // 引用箇所のハイライトは、MarkdownView が innerHTML を描いたあとに被せる（子の effect が
  // 先に走るので、この effect の時点では本文が出来ている）。本文もコメントも変わらない
  // 再描画では MarkdownView 側が何もしないため、被せたマークはそのまま残る。
  useEffect(() => {
    const root = bodyRef.current?.querySelector<HTMLElement>(".markdown");
    if (!root) return;
    applyQuoteMarks(root, key ? comments.map((c) => ({ quote: c.quote, nth: c.nth })) : []);
  }, [content, comments, key]);

  // 選択が確定したらピルを出す。タッチ選択（長押し＋ドラッグ）は mouseup を出さないので
  // selectionchange でも拾う（デバウンス。ReaderView / FileView と同じ作法）。
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
  const captureRef = useRef(capture);
  captureRef.current = capture;
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const onSelChange = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => captureRef.current(), 250);
    };
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      if (timer) clearTimeout(timer);
    };
  }, []);

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
      <header className="view-head fileinfo">
        <span className="fi-name mono">
          <Icon name="checklist" /> {title || tr("view.document")}
        </span>
        <span className="fi-tag">Markdown</span>
        {key && (
          <span className="fi-tag doc-review-tag" title={tr("plan.review_hint")}>
            <Icon name="comment-discussion" /> {tr("plan.review_tag")}
          </span>
        )}
      </header>
      <div className="md-scroll" ref={bodyRef} onMouseUp={() => captureRef.current()}>
        <MarkdownView
          source={content || ""}
          onOpenFile={(path, line, column, openInNew) => {
            const target = { content: { kind: "file" as const, filePath: path, targetLine: line, targetColumn: column } };
            if (openInNew) openTargetInNew(target, true);
            else openTarget(target);
          }}
          onOpenDir={revealInFiles}
        />
      </div>

      {key && comments.length > 0 && (
        // 溜まっているコメントの控え。送信はミラー側なので、ここでは確認と削除だけ。
        <div className={"doc-comments" + (listOpen ? "" : " collapsed")}>
          <button type="button" className="doc-comments-head" onClick={() => setListOpen((v) => !v)}>
            <Icon name={listOpen ? "chevron-down" : "chevron-up"} />
            <span>{tr("plan.comments_count", { count: comments.length })}</span>
            <span className="muted doc-comments-hint">{tr("plan.send_from_mirror")}</span>
          </button>
          {listOpen && (
            <ol className="doc-comments-list">
              {comments.map((c, i) => (
                <li key={c.id} className={"doc-comment" + (c.sentAt ? " sent" : "")}>
                  <span className="doc-comment-n">{i + 1}</span>
                  <span className="doc-comment-main">
                    <span className="doc-comment-quote">{c.quote}</span>
                    <span className="doc-comment-body">{c.body}</span>
                  </span>
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
              ))}
            </ol>
          )}
        </div>
      )}

      {/* body へポータル: .fileview は container-type を持つため、中に置いた position:fixed は
          ビューポートではなくペイン基準になってしまう（FileView の選択ピルと同じ理由）。 */}
      {pill &&
        !draft &&
        createPortal(
          <div className="sel-pill-group" style={{ left: pill.x, top: Math.max(4, pill.y) }}>
            <button
              type="button"
              className="sel-send-pill"
              onMouseDown={(e) => e.preventDefault()} // クリックで選択が消えないように
              onClick={startDraft}
              disabled={full}
              title={full ? tr("plan.comments_full") : undefined}
            >
              <Icon name="comment-discussion" /> {tr("plan.add_comment")}
            </button>
          </div>,
          document.body,
        )}
      {draft &&
        createPortal(
          <div className="doc-comment-pop" style={{ left: draft.x, top: Math.max(4, draft.y) }}>
            <div className="doc-comment-pop-quote">{draft.quote}</div>
            <textarea
              className="doc-comment-pop-input"
              autoFocus
              rows={3}
              value={body}
              placeholder={tr("plan.comment_placeholder")}
              onChange={(e) => setBody(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  closeDraft();
                } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
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
