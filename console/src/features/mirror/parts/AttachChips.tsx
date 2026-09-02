import { Icon } from "../../../ui/Icon.tsx";
import FileIcon from "../../../ui/FileIcon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/** 送信待ちの添付。`path` はセッションに保存された絶対パス（プロンプト本文が参照する）。 */
export type Attachment = { path: string; name: string; url: string; image: boolean };

/** 添付チップの列。画像はサムネイル、それ以外はアイコン＋ファイル名。 */
export function AttachChips({
  attachments,
  pasting,
  onRemove,
}: {
  attachments: Attachment[];
  pasting: boolean;
  onRemove: (i: number) => void;
}) {
  if (!attachments.length && !pasting) return null;
  return (
    <div className="mirror-attach">
      {attachments.map((a, i) => (
        <div className={"ma-chip" + (a.image ? "" : " ma-file")} key={a.path}>
          {a.image ? (
            <img className="ma-thumb" src={a.url} alt="" />
          ) : (
            <span className="ma-fname" title={a.name}>
              <FileIcon name={a.name} />
              <span className="ma-fname-text">{a.name}</span>
            </span>
          )}
          <button type="button" className="ma-del" title={tr("chat.remove")} onClick={() => onRemove(i)}>
            <Icon name="close" />
          </button>
        </div>
      ))}
      {pasting && (
        <span className="ma-loading">
          <Icon name="loading" spin /> {tr("chat.uploading")}
        </span>
      )}
    </div>
  );
}
