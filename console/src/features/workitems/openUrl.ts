import { dedupeWorkItems, sortWorkItems } from "./read.ts";
import { useWorkItemStore } from "./store.ts";
import { useWorkItemModal } from "./modal.ts";
import { itemsForTicketUrl, resolveWorkItemRef, workItemRefOfUrl } from "./refs.ts";

/** openWorkItemUrl opens the work item detail modal for a tracker URL that names one ticket and
 *  returns true; false means the URL is not a ticket page and the caller must let the link go to
 *  the tracker. The cache is read now, not when the link was drawn: a row may have arrived since.
 *  A ticket that is not cached opens as the reference stand-in, keeping the URL that was linked
 *  (a pull request must not become a constructed `/issues/N`). */
export function openWorkItemUrl(href: string): boolean {
  const items = dedupeWorkItems(sortWorkItems(useWorkItemStore.getState().payload?.items || []));
  const ref = workItemRefOfUrl(href, items);
  if (!ref) return false;
  const resolved = resolveWorkItemRef(ref, itemsForTicketUrl(href, items));
  const item = resolved.reference ? { ...resolved.item, url: href } : resolved.item;
  useWorkItemModal.getState().openDetail(item, { reference: resolved.reference });
  return true;
}
