// The line a note shows for the work item it points at: the provider's mark, the item's key as a
// link to its address and, when the work-item cache has the item, its title. The title is read
// from the cache at draw time and never stored with the note, so an item that is not cached shows
// the key alone and nothing else of it.
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useWorkItemStore } from "../workitems/store.ts";
import { PROVIDER_NAMES } from "../workitems/read.ts";
import type { MemoReference } from "../../types/memo.ts";
import { findWorkItem, linkableUrl, referenceIcon } from "./reference.ts";

export function MemoReferenceLink({ reference }: { reference: MemoReference }) {
  const tr = useT();
  const items = useWorkItemStore((s) => s.payload?.items);
  const item = items ? findWorkItem(reference, items) : undefined;
  const provider = PROVIDER_NAMES[reference.provider] || reference.provider;
  return (
    <div className="memo-reflink">
      <span className="memo-reflink-mark" title={provider}>
        <Icon name={referenceIcon(item)} />
      </span>
      {linkableUrl(reference.url) ? (
        <a
          className="memo-reflink-key"
          href={reference.url}
          target="_blank"
          rel="noreferrer noopener"
          title={tr("memo.ref_open_title", { key: reference.key, provider })}
          // A row's body toggles and edits on a click; following the link must not.
          onClick={(e) => e.stopPropagation()}
        >
          {item?.key || reference.key}
        </a>
      ) : (
        // Drawn all the same, as text: the note holds it and the approval is bound to it.
        <span className="memo-reflink-key" title={reference.url}>
          {reference.key || reference.url}
        </span>
      )}
      {item?.title && <span className="memo-reflink-title">{item.title}</span>}
    </div>
  );
}
