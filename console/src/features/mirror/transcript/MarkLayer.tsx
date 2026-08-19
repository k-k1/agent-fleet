// transcript/MarkLayer — 選択したところに線を引く／引いた線を確認して消す（docs/69）。
//
// 転写ぜんぶで 1 つだけ載る浮遊レイヤー。ターンごとに置かないのは、選択もクリックも
// document 単位の出来事で、ターンの数だけ購読を張ると 400 ターンぶんの listener になるから。
//
// 位置決めは placeFixed（＋メニューと同じ道具）。選択の採取は selectionchange のデバウンス
// ——タッチの長押し選択は mouseup を出さないので、そちらだけでは拾えない（プランコメントと
// 同じ作法）。

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { placeFixed } from "../../../lib/placeFixed.ts";
import { selectionAnchor } from "../../viewer/quoteMarks.ts";
import { MARK_CLASS } from "./markPaint.ts";
import { MARKABLE_KINDS, MARK_COLORS, parseRootKey, type MarkColor, type TranscriptMark } from "./marks.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";
import { formatTS } from "./blocks.tsx";

// 色名は t() のキーが literal であることを型で守るため、動的な連結ではなく表で引く。
const COLOR_LABEL: Record<MarkColor, () => string> = {
  yellow: () => tr("mirror.mark.color.yellow"),
  green: () => tr("mirror.mark.color.green"),
  blue: () => tr("mirror.mark.color.blue"),
  pink: () => tr("mirror.mark.color.pink"),
};

/** 選択の確定を待つ間。プランコメントと同じ。 */
const SELECT_DEBOUNCE = 250;
/** 選択の少し上にピルを出す。 */
const PILL_OFFSET = 40;

interface Draft {
  root: string;
  kind: string;
  quote: string;
  nth: number;
  x: number;
  y: number;
}

interface Card {
  mark: TranscriptMark;
  x: number;
  y: number;
}

/** 選択の両端を含む、印を置ける要素。片側でも外なら null（引用が root をまたぐ選択）。 */
function rootOfSelection(): HTMLElement | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const range = sel.getRangeAt(0);
  const from = range.startContainer;
  const el = from.nodeType === Node.ELEMENT_NODE ? (from as Element) : from.parentElement;
  const root = el?.closest<HTMLElement>("[data-mark-root]") || null;
  if (!root || !root.contains(range.endContainer)) return null;
  return root;
}

export function MarkLayer({ marks }: { marks: TranscriptMarksWiring }) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [card, setCard] = useState<Card | null>(null);
  const pillRef = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  const capture = useCallback(() => {
    if (!marks.canEdit) return;
    const root = rootOfSelection();
    if (!root) {
      setDraft(null);
      return;
    }
    const kind = root.dataset.markKind ?? "";
    // 置ける kind は Agent 側と同じ表で閉じている（docs/69 §69.4）。描ける場所が増えても、
    // 座標を持つ part の上には出さない。
    if (!MARKABLE_KINDS.has(kind)) {
      setDraft(null);
      return;
    }
    const anchor = selectionAnchor(root);
    if (!anchor) {
      setDraft(null);
      return;
    }
    setDraft({
      root: root.dataset.markRoot || "",
      kind,
      quote: anchor.quote,
      nth: anchor.nth,
      x: Math.round(anchor.rect.left),
      y: Math.round(anchor.rect.top - PILL_OFFSET),
    });
  }, [marks]);

  const captureRef = useRef(capture);
  captureRef.current = capture;
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const onSel = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => captureRef.current(), SELECT_DEBOUNCE);
    };
    document.addEventListener("selectionchange", onSel);
    return () => {
      document.removeEventListener("selectionchange", onSel);
      if (timer) clearTimeout(timer);
    };
  }, []);

  // 引いてある線をクリックしたら、誰がいつ引いたかを出す（消せる人には消す導線も）。
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      const el = (e.target as Element | null)?.closest?.<HTMLElement>("mark." + MARK_CLASS);
      if (!el) {
        if (!(e.target as Element | null)?.closest?.(".tmark-card")) setCard(null);
        return;
      }
      const mark = marks.find(el.dataset.markId || "");
      if (!mark) return;
      const rect = el.getBoundingClientRect();
      setCard({ mark, x: Math.round(rect.left), y: Math.round(rect.bottom + 6) });
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [marks]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setDraft(null);
      setCard(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // 浮遊要素は描画されてから実測して寄せる（右端の選択でボタンが画面外へ出ないように）。
  useEffect(() => {
    if (draft && pillRef.current) placeFixed(pillRef.current, draft.x, draft.y);
  }, [draft]);
  useEffect(() => {
    if (card && cardRef.current) placeFixed(cardRef.current, card.x, card.y);
  }, [card]);

  const paint = (color: MarkColor) => {
    if (!draft) return;
    const parsed = parseRootKey(draft.root);
    if (parsed) {
      marks.add({ turn: parsed.turn, part: parsed.part, kind: draft.kind, quote: draft.quote, nth: draft.nth, color });
    }
    setDraft(null);
    window.getSelection()?.removeAllRanges();
  };

  return createPortal(
    <>
      {draft && (
        <div className="tmark-pill" ref={pillRef} role="group" aria-label={tr("mirror.mark.pill")}>
          {MARK_COLORS.map((c) => (
            <button
              key={c}
              type="button"
              className={"tmark-swatch tmark-" + c}
              title={tr("mirror.mark.paint", { color: COLOR_LABEL[c]() })}
              aria-label={tr("mirror.mark.paint", { color: COLOR_LABEL[c]() })}
              onMouseDown={(e) => e.preventDefault()} // 選択を保ったままクリックさせる
              onClick={() => paint(c)}
            />
          ))}
        </div>
      )}
      {card && (
        <div className="tmark-card" ref={cardRef}>
          <div className="tmark-card-who">
            <span className={"tmark-dot tmark-a" + marks.authorSlot(card.mark.author)} />
            {marks.authorLabel(card.mark.author)}
            {card.mark.created_at ? (
              <span className="muted"> · {formatTS(new Date(card.mark.created_at).toISOString())}</span>
            ) : null}
          </div>
          <div className="tmark-card-quote">{card.mark.quote}</div>
          {marks.canRemove(card.mark) && (
            <button
              type="button"
              className="ghost xs tmark-card-del"
              onClick={() => {
                marks.remove(card.mark.id);
                setCard(null);
              }}
            >
              <Icon name="trash" /> {tr("mirror.mark.remove")}
            </button>
          )}
        </div>
      )}
    </>,
    document.body,
  );
}
