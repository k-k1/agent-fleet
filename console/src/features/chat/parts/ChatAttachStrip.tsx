import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// ChatAttachStrip is the row of pasted-image chips above the composer, plus the
// "uploading" marker while a paste is still in flight. Nothing is sent yet — these are
// the paths the next prompt will reference.
export function ChatAttachStrip({
  attachments,
  pasting,
  onRemove,
}: {
  attachments: { path: string; name: string; url: string }[];
  pasting: boolean;
  onRemove: (i: number) => void;
}) {
  const tr = useT();
  return (
    <div className="chat-attach">
      {attachments.map((a, i) => (
        <div className="ca-chip" key={a.path}>
          <img className="ca-thumb" src={a.url} alt="" />
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
