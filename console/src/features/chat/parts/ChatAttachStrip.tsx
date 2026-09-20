import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// ChatAttachStrip is the row of pasted-image chips above the composer, plus the
// "uploading" marker while a paste is still in flight. Nothing is sent yet — these are
// the paths the next prompt will reference.
export function ChatAttachStrip({
  attachments,
  pasting,
  onRemove,
  onOpen,
}: {
  attachments: { path: string; name: string; url: string }[];
  pasting: boolean;
  onRemove: (i: number) => void;
  /** Opens a chip's image in the shared lightbox; ChatView owns the state. */
  onOpen: (url: string) => void;
}) {
  const tr = useT();
  return (
    <div className="chat-attach">
      {attachments.map((a, i) => (
        <div className="ca-chip" key={a.path}>
          <button type="button" className="ca-thumb-btn" title={tr("chat.click_to_zoom")} onClick={() => onOpen(a.url)}>
            <img className="ca-thumb" src={a.url} alt="" />
          </button>
          <button type="button" className="ca-del" title={tr("chat.remove")} onClick={() => onRemove(i)}>
            <Icon name="close" />
          </button>
        </div>
      ))}
      {pasting && (
        <span className="ca-loading">
          <Icon name="loading" spin /> {tr("chat.uploading")}
        </span>
      )}
    </div>
  );
}
