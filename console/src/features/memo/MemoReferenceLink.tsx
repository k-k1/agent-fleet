// The line a note shows for the work item it points at: the provider's mark, the item's key as a
// link to its address (or, with `inApp`, a button opening the in-app detail modal: the cached
// item, else a reference-only stand-in the modal says is not in the inbox) and, when the work-item cache has the item, its title. The title is read
// from the cache at draw time and never stored with the note, so an item that is not cached shows
// the key alone and nothing else of it.
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useWorkItemStore } from "../workitems/store.ts";
import { useWorkItemModal } from "../workitems/modal.ts";
import { PROVIDER_NAMES } from "../workitems/read.ts";
import { resolveWorkItemRef } from "../workitems/refs.ts";
import type { MemoReference } from "../../types/memo.ts";
import { findWorkItem, linkableUrl, referenceIcon } from "./reference.ts";

/** `trailing` is drawn on the same line, after the title (the row's state and producer).
 * `inApp` makes the key open the in-app detail modal, for an uncached item too (a stand-in built
 * from the key; the modal has its own way to the tracker). Only a provider the modal does not
 * know keeps the external link, drawn with an external-link mark; the approval card and the
 * edit view leave it off so the address being approved stays the visible link. */
export function MemoReferenceLink({
  reference,
  trailing,
  inApp,
}: {
  reference: MemoReference;
  trailing?: React.ReactNode;
  inApp?: boolean;
}) {
  const tr = useT();
  const items = useWorkItemStore((s) => s.payload?.items);
  const item = items ? findWorkItem(reference, items) : undefined;
  const provider = PROVIDER_NAMES[reference.provider] || reference.provider;
  // A key the modal can open without a cached row: a known provider and a key to build from.
  const openable = !!item || (!!reference.key && Object.hasOwn(PROVIDER_NAMES, reference.provider));
  return (
    <div className="memo-reflink">
      <span className="memo-reflink-mark" title={provider}>
        <Icon name={referenceIcon(item)} />
      </span>
      {inApp && openable ? (
        // The in-app detail modal; an unknown provider or a key-less note falls to the link below.
        <button
          type="button"
          className="memo-reflink-key linkish"
          title={tr("memo.ref_modal_title", { key: reference.key })}
          onClick={(e) => {
            e.stopPropagation();
            // The row matched at draw time, not a fresh lookup by the note's key: the note may
            // hold the key in another case than the cache does.
            if (item) {
              useWorkItemModal.getState().openDetail(item);
              return;
            }
            // Not cached: a stand-in carrying the key, with the address the note holds when it is
            // a safe one. The modal marks it as not in the inbox.
            const r = resolveWorkItemRef({ provider: reference.provider, key: reference.key }, items ?? []);
            const url = linkableUrl(reference.url) ? reference.url : r.item.url;
            useWorkItemModal.getState().openDetail(r.reference ? { ...r.item, url } : r.item, { reference: r.reference });
          }}
        >
          {item?.key || reference.key}
        </button>
      ) : linkableUrl(reference.url) ? (
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
          {inApp && <Icon name="link-external" className="memo-reflink-ext" />}
        </a>
      ) : (
        // Drawn all the same, as text: the note holds it and the approval is bound to it.
        <span className="memo-reflink-key" title={reference.url}>
          {reference.key || reference.url}
        </span>
      )}
      {item?.title && <span className="memo-reflink-title">{item.title}</span>}
      {trailing}
    </div>
  );
}
