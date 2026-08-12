export type PaneDropZone = "center" | "down" | "right";

/** A lone Cell cannot be dragged as geometry, but its tab can still create the
 * second Cell. Keep those permissions independent. */
export function acceptsPaneDrag(canDragCell: boolean, hasCellPayload: boolean, hasTabPayload: boolean): boolean {
  return hasTabPayload || (canDragCell && hasCellPayload);
}

/** Tab buttons own center drops (reorder); edge drops bubble to the Cell. */
export const tabOwnsDrop = (zone: PaneDropZone): boolean => zone === "center";

export const TAB_DRAGGING_CLASS = "tab-dragging";

/** Publish tab dragging globally so every Cell can shield its scrollable View,
 * including Cells other than the drag source. */
export function setTabDragShield(active: boolean, root: Pick<DOMTokenList, "add" | "remove"> = document.body.classList): void {
  root[active ? "add" : "remove"](TAB_DRAGGING_CLASS);
}
