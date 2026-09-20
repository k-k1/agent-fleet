import { useEffect, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { raw } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";

// ChatPastedThumb previews a pasted image referenced in a chat turn. It fetches the bytes
// through the authenticated API wrapper (an <img src> can't carry the tenant header) into
// an object URL; clicking hands that URL to the caller's lightbox (ChatView owns the state,
// same pattern as the mirror's PastedThumb).
export function ChatPastedThumb({
  convId,
  name,
  onOpen,
}: {
  convId: string;
  name: string;
  onOpen: (url: string) => void;
}) {
  const tr = useT();
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let alive = true;
    let obj = "";
    raw(`api/chat/conversations/${encodeURIComponent(convId)}/pasted/${encodeURIComponent(name)}`)
      .then((r) => (r.ok ? r.blob() : null))
      .then((b) => {
        if (!alive) return;
        if (!b) {
          setFailed(true);
          return;
        }
        obj = URL.createObjectURL(b);
        setUrl(obj);
      })
      .catch(() => {
        if (alive) setFailed(true);
      });
    return () => {
      alive = false;
      if (obj) URL.revokeObjectURL(obj);
    };
  }, [convId, name]);
  if (failed) {
    return (
      <span className="chat-img chat-img-loading" title={tr("chat.preview_failed")}>
        <Icon name="file-media" />
      </span>
    );
  }
  if (!url) {
    return (
      <span className="chat-img chat-img-loading">
        <Icon name="loading" spin />
      </span>
    );
  }
  return (
    <button type="button" className="chat-img" title={tr("chat.click_to_zoom")} onClick={() => onOpen(url)}>
      <img src={url} alt={tr("chat.pasted_image_alt")} />
    </button>
  );
}
