// Scroll sync between the editor and the preview of the File pane's split view.
//
// Both sides are positioned by source line: the editor reports the fractional line at its
// viewport top, and the preview's blocks carry the line they were rendered from
// (mdSourceLines.ts). Between two anchors the offset is interpolated, so a long paragraph
// or a tall diagram scrolls proportionally rather than in jumps.
//
// Only the side the user is driving is followed. A programmatic scroll of the follower fires
// its own scroll event, and following that back is the feedback loop that makes split views
// jitter; the driver changes only on real input (wheel, pointer, touch, key) on a side.
import { SOURCE_LINE_ATTR } from "./mdSourceLines.ts";

/** A preview block's 1-based source line and its offset in the scroller's content. */
export interface LineAnchor {
  line: number;
  top: number;
}

/** Keep the anchors that increase in both line and offset. Blocks that are hidden, nested
 *  out of order or floated would otherwise make the mapping run backwards. */
export function monotoneAnchors(anchors: readonly LineAnchor[]): LineAnchor[] {
  const sorted = [...anchors].sort((a, b) => a.line - b.line || a.top - b.top);
  const out: LineAnchor[] = [];
  for (const a of sorted) {
    const last = out[out.length - 1];
    if (!last) out.push(a);
    else if (a.line > last.line && a.top > last.top) out.push(a);
  }
  return out;
}

/** The preview offset of a fractional source line. `anchors` must be monotone and start at
 *  {line: 1, top: 0} (the caller adds that and the end sentinel). */
export function lineToTop(anchors: readonly LineAnchor[], line: number): number {
  if (!anchors.length) return 0;
  if (line <= anchors[0]!.line) return anchors[0]!.top;
  for (let i = 1; i < anchors.length; i++) {
    const b = anchors[i]!;
    if (line < b.line) {
      const a = anchors[i - 1]!;
      return a.top + ((line - a.line) / (b.line - a.line)) * (b.top - a.top);
    }
  }
  return anchors[anchors.length - 1]!.top;
}

/** The inverse of lineToTop: the fractional source line at a preview offset. */
export function topToLine(anchors: readonly LineAnchor[], top: number): number {
  if (!anchors.length) return 1;
  if (top <= anchors[0]!.top) return anchors[0]!.line;
  for (let i = 1; i < anchors.length; i++) {
    const b = anchors[i]!;
    if (top < b.top) {
      const a = anchors[i - 1]!;
      return a.line + ((top - a.top) / (b.top - a.top)) * (b.line - a.line);
    }
  }
  return anchors[anchors.length - 1]!.line;
}

/** What the sync needs from the editor (CodeEditorHandle provides it). */
export interface SyncEditor {
  scroller: HTMLElement;
  /** Input anywhere in here (the search panel too) makes the editor the driver. */
  host: HTMLElement;
  topLine(): number;
  scrollToLine(line: number): void;
  lineCount(): number;
}

export type SyncSide = "editor" | "preview";

/** Measure the anchors of the preview content inside `scroller`, with the start and end
 *  sentinels that let the first and last stretches interpolate too. */
export function measureAnchors(scroller: HTMLElement, lineCount: number): LineAnchor[] {
  const base = scroller.getBoundingClientRect().top - scroller.scrollTop;
  const found: LineAnchor[] = [{ line: 1, top: 0 }];
  for (const el of scroller.querySelectorAll<HTMLElement>(`[${SOURCE_LINE_ATTR}]`)) {
    const rect = el.getBoundingClientRect();
    if (!rect.width && !rect.height) continue; // display: none, or inside a closed <details>
    const line = Number(el.getAttribute(SOURCE_LINE_ATTR));
    if (Number.isFinite(line)) found.push({ line, top: rect.top - base });
  }
  found.push({ line: lineCount + 1, top: Math.max(scroller.scrollHeight, 1) });
  return monotoneAnchors(found);
}

const atTop = (el: HTMLElement) => el.scrollTop <= 1;
const atBottom = (el: HTMLElement) => el.scrollTop >= el.scrollHeight - el.clientHeight - 1;

/** The installed sync. `lead` hands the driver to a side for a change made without input
 *  on it — an accepted AI suggestion is applied from a panel outside both sides. */
export interface SplitScrollSync {
  lead(side: SyncSide): void;
  dispose(): void;
}

/** Install the sync on `container`, which holds both the editor and the preview. The
 *  preview scroller is looked up on every pass: the renderer replaces it when the file
 *  reloads. `initial` is the side whose position is kept when the split opens — the one that
 *  was on screen before. */
export function installSplitScrollSync(
  container: HTMLElement,
  editor: SyncEditor,
  findPreview: () => HTMLElement | null,
  initial: SyncSide = "editor",
): SplitScrollSync {
  let driver: SyncSide = initial;
  let frame = 0;
  let observed: Element | null = null;
  // Anchor offsets are in content coordinates, so scrolling never moves them: measure once
  // and re-measure only when the content changes (a scroll pass would otherwise query and
  // measure every tagged block of the document each frame).
  let anchors: LineAnchor[] | null = null;
  let anchorsFor: HTMLElement | null = null;

  const sync = () => {
    frame = 0;
    const preview = findPreview();
    if (!preview) return;
    observe(preview);
    const lead = driver === "editor" ? editor.scroller : preview;
    const follow = driver === "editor" ? preview : editor.scroller;
    // The ends are pinned outright: the last line at the top of the editor is rarely the
    // last block at the top of the preview, so interpolation alone never reaches either end.
    if (atTop(lead)) {
      follow.scrollTop = 0;
      return;
    }
    if (atBottom(lead)) {
      follow.scrollTop = follow.scrollHeight;
      return;
    }
    if (!anchors || anchorsFor !== preview) {
      anchors = measureAnchors(preview, editor.lineCount());
      anchorsFor = preview;
    }
    if (driver === "editor") preview.scrollTop = lineToTop(anchors, editor.topLine());
    else editor.scrollToLine(topToLine(anchors, preview.scrollTop));
  };
  const schedule = () => {
    if (!frame) frame = requestAnimationFrame(sync);
  };
  const remeasure = () => {
    anchors = null;
    schedule();
  };

  // A re-render (debounced typing), a mermaid diagram or an image landing changes the
  // preview's height under a still scroll position; realign from whichever side leads. The
  // mutation observer catches a re-render that happens to keep the height.
  const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(remeasure);
  const mutation = typeof MutationObserver === "undefined" ? null : new MutationObserver(remeasure);
  const observe = (preview: HTMLElement) => {
    const content = preview.firstElementChild;
    if (!content || content === observed) return;
    if (observed) resize?.unobserve(observed);
    resize?.observe(content);
    mutation?.disconnect();
    mutation?.observe(content, { childList: true });
    observed = content;
  };

  const sideOf = (target: EventTarget | null): SyncSide | null => {
    if (!(target instanceof Node)) return null;
    if (editor.host.contains(target)) return "editor";
    const preview = findPreview();
    return preview && preview.contains(target) ? "preview" : null;
  };
  const claim = (event: Event) => {
    const side = sideOf(event.target);
    if (side) driver = side;
  };
  const onScroll = (event: Event) => {
    // Scroll does not bubble; this listens in the capture phase, so inner scrollers (a wide
    // table, a code block) reach it too and are told apart from the two that matter here.
    const target = event.target;
    const side = target === editor.scroller ? "editor" : target === findPreview() ? "preview" : null;
    if (side && side === driver) schedule();
  };

  const input = ["wheel", "pointerdown", "touchstart", "keydown"] as const;
  for (const type of input) container.addEventListener(type, claim, { capture: true, passive: true });
  container.addEventListener("scroll", onScroll, { capture: true, passive: true });
  // Opening the split: wait for layout (the editor was hidden in preview mode, and
  // CodeMirror measures on the frame after it is shown).
  let openFrame = requestAnimationFrame(() => {
    openFrame = requestAnimationFrame(() => {
      openFrame = 0;
      sync();
    });
  });

  return {
    lead(side) {
      driver = side;
      schedule();
    },
    dispose() {
      for (const type of input) container.removeEventListener(type, claim, { capture: true });
      container.removeEventListener("scroll", onScroll, { capture: true });
      if (frame) cancelAnimationFrame(frame);
      if (openFrame) cancelAnimationFrame(openFrame);
      resize?.disconnect();
      mutation?.disconnect();
    },
  };
}
