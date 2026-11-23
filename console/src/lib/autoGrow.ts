// Sizes the composer's textarea to its content (the cap is the CSS max-height; past it the
// textarea scrolls internally). Used by three inputs: the mirror, the assistant chat and the memo
// queue.
//
// The naive form - set height:auto, read scrollHeight, write it back into height - leaks the
// shrink to two rows that happens while measuring. The mirror composer is a sibling of the
// transcript (.mirror-body, flex:1), so for that instant the transcript's clientHeight grows by
// the input's height and the browser clamps the scrollTop of a view pinned to the bottom.
// Restoring the height does not restore scrollTop, so the view drifts off the bottom on every
// keystroke, by "input height - 2 rows": the taller the input, the larger the drift (measured:
// 154px, enough for the jump-to-latest control to appear).
//
// Chromium's scroll anchoring cancels the clamp, so it stays invisible there, but that is luck,
// not a guarantee: an engine without anchoring, or with it suppressed, shows it directly
// (measured: with overflow-anchor:none on .mirror-body the first keystroke gives gap=154px, and
// the scripts/mirror-scroll typing scenario runs under exactly that condition).
//
// So while measuring, the input's parent (the composer row) is pinned with min-height and the
// shrink cannot escape. It only prevents shrinking, so overestimating by a few px by passing the
// border-box value is harmless: scrollTop is never clamped in the direction of a shrinking box.
//
// An empty input takes its `rows` height instead of being measured: Chromium counts the placeholder
// in scrollHeight, so an empty measure is "how many lines the placeholder wraps to at this width",
// and the value then sticks until the next draft change. Seen on a phone in the image studio:
// the composer came back one line tall with the placeholder's second line cut off.
//
// A textarea that offers a drag handle passes `respectResize`. The handle writes an inline height
// that autoGrow did not write, which is how a member's own size is told from ours: while the inline
// height differs from the last one written here, the member's size is kept (no keystroke snaps it
// back). Emptying it starts over, so the next draft grows again. A touch browser has no handle, so
// its height is only ever ours. The box is border-box, so its borders are added to the content's
// scrollHeight; the stylesheet gives it `overflow-y: auto`, which takes over past the CSS
// max-height and straight after a drag to a smaller size.
export function autoGrowTextarea(el: HTMLTextAreaElement | null, opts?: { respectResize?: boolean }): void {
  if (!el) return;
  const respect = !!opts?.respectResize;
  if (!el.value) {
    el.style.height = "";
    if (respect) {
      delete el.dataset.grown;
      delete el.dataset.resized;
    }
    return;
  }
  if (respect) {
    const h = el.style.height;
    if (el.dataset.resized || (h && h !== el.dataset.grown)) {
      el.dataset.resized = "1";
      return;
    }
  }
  const row = el.parentElement;
  const frozen = row ? Math.ceil(row.getBoundingClientRect().height) : 0;
  const prevMin = row ? row.style.minHeight : "";
  if (row && frozen > 0) row.style.minHeight = frozen + "px";
  try {
    el.style.height = "auto";
    // offsetHeight - clientHeight is the borders (a border-box height must include them).
    el.style.height = el.scrollHeight + (respect ? el.offsetHeight - el.clientHeight : 0) + "px";
    if (respect) el.dataset.grown = el.style.height;
  } finally {
    if (row && frozen > 0) row.style.minHeight = prevMin;
  }
}
