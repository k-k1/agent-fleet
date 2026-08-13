// DocView — in-memory Markdown (e.g. a plan) in its own pane, no file on disk.
// The content lives in the pane descriptor. Port of views/DocView.
//
// プランとして開かれた（docSession を持つ）ときは「レビュー面」になる: 本文を選択すると
// コメント追加のピルが出て、付けたコメントは引用箇所にハイライトが付く。溜めるだけで、
// 送信はミラーのプランカードが担う（送り先の状態＝承認待ちか却下後かで経路が変わるため、
// 判断はセッションの状態を持っている側に置く）。
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
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
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

interface PendingComment {
  quote: string;
  nth: number;
  x: number;
  y: number;
}

/** 選択ピルを選択の少し上に出すための持ち上げ量（FileView と同じ値）。 */
const PILL_OFFSET = 34;
/** 画面端との最小余白。 */
const EDGE = 8;
/** 控えの一覧に一度に見せる件数（超えたぶんはスクロール）。 */
const VISIBLE_COMMENTS = 5;

// clampFixed は position:fixed の浮遊要素を、実測サイズでビューポート内へ寄せる。
// 選択位置をそのまま left/top にすると、右端や下端の選択でポップが画面外へ出て
// ボタンに手が届かなくなる（実測: 右端の行を選ぶと「追加」が切れる）。描画後に測って
// から寄せる必要があるので、state ではなく style を直接書く（再描画ループを作らない）。
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
  // 選択に添えるピル（null = 出さない）と、ピルから開いたコメント入力。
  const [pill, setPill] = useState<PendingComment | null>(null);
  const [draft, setDraft] = useState<PendingComment | null>(null);
  const [body, setBody] = useState("");
  const [listOpen, setListOpen] = useState(true);
  // 全文表示に開いたコメント（引用も本文も畳まずに出す）。既定は畳んだ一覧。
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const pillRef = useRef<HTMLDivElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLOListElement>(null);
  // コンポーザーと同じ送信キー設定に従う（Ctrl+Enter か Enter か。lib/settings mirrorSend）。
  const modSend = settings.mirrorSend !== "enter";

  // 控えの一覧は5件ぶんの高さで打ち切る。行の高さはコメントの長さ（引用1行＋本文2行まで）で
  // 変わるので、CSS の固定値では「5件」にならない — 6件目の位置から実測して決める。全文表示に
  // 開いた行があっても伸びすぎないよう、画面の 40% で頭打ちにする。
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

  // 浮遊要素は描画されてから実測して寄せる。開いている間の resize でも追従させる
  // （ペイン分割やスマホの回転で画面が縮んでも押せなくならないように）。
  useLayoutEffect(() => {
    if (!pill || draft) return;
    const fit = () => clampFixed(pillRef.current, pill.x, pill.y);
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [pill, draft]);
  useLayoutEffect(() => {
    if (!draft) return;
    const fit = () => clampFixed(popRef.current, draft.x, draft.y);
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [draft]);

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
        {headerActions && <span className="view-head-actions">{headerActions}</span>}
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
            // 5件ぶんの高さで打ち切ってスクロール（本文の邪魔をしない）。各行は畳んだ状態で
            // 高さが揃うので、この件数指定が実際の見え方と一致する。
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

      {/* body へポータル: .fileview は container-type を持つため、中に置いた position:fixed は
          ビューポートではなくペイン基準になってしまう（FileView の選択ピルと同じ理由）。 */}
      {pill &&
        !draft &&
        createPortal(
          <div className="sel-pill-group" ref={pillRef} style={{ left: pill.x, top: Math.max(EDGE, pill.y) }}>
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
                // 送信キーはコンポーザーと同じ設定に従う。IME の変換確定 Enter は横取りしない。
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
