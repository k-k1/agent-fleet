import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/**
 * 入力欄のすぐ上に浮くピル。sticky で本文の最後に置く（bottom 指定の sticky は
 * 「本来の位置より下へ行きそうなときだけ上へ留める」ので、先頭に置くと二度と
 * 降りてこない — 実測で本文の 42,000px 上に取り残された）。
 *
 * ラッパは height:0、ボタンはその中で absolute。in-flow のまま置くと、はみ出した
 * ボタンぶん（実測 12px）がスクロール可能領域を伸ばし、末尾に貼り付いているのに
 * 12px の余白が残る。「最新へ」は末尾から離れたときしか出ないので誰も踏まなかったが、
 * 「返信を頭から」は末尾でも出るので表に出た。bottom:0 の absolute なら、ボタンの箱は
 * ラッパの上へ伸びる＝末尾より下へはみ出さない。
 *
 * 「返信を頭から」は逆向きの導線で、条件も別（最新の回答の先頭が画面より上にある）。
 * 同じ帯に並べる — 両方出る場面（回答の途中を読んでいて、かつ末尾から離れている）
 * では、上へ・下への 2 択がそのまま並んで見える。
 */
export function JumpPills({
  showJump,
  showReplyTop,
  onJumpBottom,
  onJumpReplyTop,
}: {
  showJump: boolean;
  showReplyTop: boolean;
  onJumpBottom: () => void;
  onJumpReplyTop: () => void;
}) {
  if (!showJump && !showReplyTop) return null;
  return (
    <div className="mirror-jump-wrap">
      <div className="mirror-jump-row">
        {showReplyTop && (
          <button
            type="button"
            // 見た目は 最新へ と同じピル。クラスを足すのは検証のため — mirror-scroll の
            // ハーネスは「最新へ が出ていないこと」で末尾着地を判定しており、素の
            // .mirror-jump が 2 種類あると区別が付かない。
            className="mirror-jump mirror-jump-top"
            onClick={onJumpReplyTop}
            title={tr("mirror.jump_reply_top")}
            aria-label={tr("mirror.jump_reply_top")}
          >
            <Icon name="arrow-up" /> {tr("mirror.jump_reply_top")}
          </button>
        )}
        {showJump && (
          <button
            type="button"
            className="mirror-jump"
            onClick={onJumpBottom}
            title={tr("mirror.jump_latest")}
            aria-label={tr("mirror.jump_latest")}
          >
            <Icon name="arrow-down" /> {tr("mirror.jump_latest")}
          </button>
        )}
      </div>
    </div>
  );
}
