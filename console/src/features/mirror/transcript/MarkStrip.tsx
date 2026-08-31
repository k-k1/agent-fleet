// transcript/MarkStrip — 引いたマーカーの一覧（docs/log/69 §69.7）。
//
// 「誰が引いたか」の主経路はここ。本文の `<mark>` は下線の色で作成者を示すだけで、名前は
// 出さない（読みを邪魔するため）。畳み方・per-session の開閉記憶・空のときは帯ごと出さない、
// は変更ファイル帯（FileChangeStrip / docs/log/68）と揃えてある — 同じ帯の中で畳み方が違う
// パネルが並ぶと、無関係な機能が2つあるように読める。
//
// 行を押すとその印までスクロールする。⚠️ ただし転写は tail 窓しか持たないので、まだ読み込んで
// いないターンの印は画面上に存在しない。そのぶんは押せない行として出す（「押しても何も
// 起きない」より、押せないほうが壊れて見えない）。

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

  // ⚠️ 空の帯を出さない。「0 件」は「まだ誰も引いていない」と「この面では引けない」を
  // 区別できず、常時場所だけ取る。
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
          {/* 2人以上が引いている会話でだけ、誰が関わっているかを畳んだままでも見せる。 */}
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
