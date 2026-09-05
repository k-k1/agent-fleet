// MemoImageThumb previews a memo's image attachment (docs/log/21 image attachments). The memo-image
// endpoint requires the tenant header (fetch injects X-AF-Tenant), so a bare <img src>
// can't reach it — we fetch the bytes as a blob into an object URL, mirroring
// MirrorView's PastedThumb. Clicking enlarges it in a lightweight overlay; an optional
// remove button turns the thumb into a composer chip. Rendered in the composer preview,
// each queued memo row, and the send modal.
import { useEffect, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { raw, memoImageURL } from "../../core/api/client.ts";

export function MemoImageThumb({ name, onRemove }: { name: string; onRemove?: () => void }) {
  const tr = useT();
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const [zoom, setZoom] = useState(false);

  useEffect(() => {
    let alive = true;
    let obj = "";
    raw(memoImageURL(name))
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
  }, [name]);

  return (
    <span className="memo-thumb-wrap">
      {failed ? (
        <span className="memo-thumb failed" title={tr("memo.image_failed")}>
          <Icon name="file-media" />
        </span>
      ) : !url ? (
        <span className="memo-thumb loading">
          <Icon name="loading" spin />
        </span>
      ) : (
        <button type="button" className="memo-thumb" title={tr("memo.image_zoom")} onClick={() => setZoom(true)}>
          <img src={url} alt={name} />
        </button>
      )}
      {onRemove && (
        <button
          type="button"
          className="memo-thumb-del"
          title={tr("memo.image_remove")}
          aria-label={tr("memo.image_remove")}
          onClick={onRemove}
        >
          <Icon name="close" />
        </button>
      )}
      {zoom && url && (
        <div className="memo-lightbox" onClick={() => setZoom(false)} role="dialog">
          <img src={url} alt={name} />
        </div>
      )}
    </span>
  );
}
