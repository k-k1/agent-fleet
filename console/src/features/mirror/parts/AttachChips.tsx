import { Icon } from "../../../ui/Icon.tsx";
import FileIcon from "../../../ui/FileIcon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { Attachment } from "../../../lib/attachDraft.ts";

/** An attachment waiting to be sent. `path` is the absolute path stored in the session, which
 *  the prompt body refers to. */
export type { Attachment };

/** The row of attachment chips: a thumbnail for images, icon plus file name otherwise. */
export function AttachChips({
  attachments,
  pasting,
  onRemove,
  onOpen,
}: {
  attachments: Attachment[];
  pasting: boolean;
  onRemove: (i: number) => void;
  /** Opens an image chip in the host's lightbox; the host owns that state (and Back). */
  onOpen?: (url: string) => void;
}) {
  if (!attachments.length && !pasting) return null;
  return (
    <div className="mirror-attach">
      {attachments.map((a, i) => (
        <div className={"ma-chip" + (a.image ? "" : " ma-file")} key={a.id}>
          {a.image && onOpen ? (
            <button type="button" className="ma-thumb-btn" title={tr("chat.click_to_zoom")} onClick={() => onOpen(a.url)}>
              <img className="ma-thumb" src={a.url} alt="" />
            </button>
          ) : a.image ? (
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
