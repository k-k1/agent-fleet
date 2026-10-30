// The contract between a pane's generic find (features/panes/PaneFind.tsx) and a view that has
// a find of its own.
//
// PaneFind scans the pane's DOM, which is wrong for a view whose text is not all in the DOM or
// that needs its own matching (the PDF pane searches every page's text content). Such a view
// marks its root with OWN_FIND_ATTR and listens for OWN_FIND_EVENT; on Ctrl/Cmd+F PaneFind then
// dispatches the event to it instead of opening its own bar.
export const OWN_FIND_ATTR = "data-own-find";
export const OWN_FIND_EVENT = "af-own-find";
