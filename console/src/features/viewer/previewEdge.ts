/** The longest edge a lightbox asks for (`displayURL`). Quantised to three steps rather than
 *  taken from the exact viewport: the Agent caches and decodes per step, so every distinct
 *  window size would otherwise be its own decode. The smallest step is already past the
 *  pictures this exists for (832x1216), which is the case where `preview` re-encodes instead of
 *  downscaling. The Agent warms a generated picture for these same steps (`previewSteps` in
 *  workspace/agent/fs_thumb.go) — change one, change both. */
export const PREVIEW_STEPS = [1024, 1536, 2048];

export function previewEdge(): number {
  const want = Math.max(window.innerWidth, window.innerHeight) * Math.min(window.devicePixelRatio || 1, 2);
  return PREVIEW_STEPS.find((step) => step >= want) ?? PREVIEW_STEPS[PREVIEW_STEPS.length - 1];
}
