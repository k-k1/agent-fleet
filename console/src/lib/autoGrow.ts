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
export function autoGrowTextarea(el: HTMLTextAreaElement | null): void {
  if (!el) return;
  const row = el.parentElement;
  const frozen = row ? Math.ceil(row.getBoundingClientRect().height) : 0;
  const prevMin = row ? row.style.minHeight : "";
  if (row && frozen > 0) row.style.minHeight = frozen + "px";
  try {
    el.style.height = "auto";
    el.style.height = el.scrollHeight + "px";
  } finally {
    if (row && frozen > 0) row.style.minHeight = prevMin;
  }
}
