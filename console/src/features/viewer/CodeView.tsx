import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, TouchEvent as RTouchEvent, SyntheticEvent } from "react";
import type { ScrollMemoryRef } from "./parts/useScrollMemory.ts";

// CodeView renders highlighted code with an optional line-number gutter and a
// VSCode-style minimap on the right edge: a scaled-down PLAIN-TEXT mirror of the file
// with a draggable viewport box. Clicking/dragging the minimap scrolls the code.
// `marks` (added/modified/deleted line numbers, from /fs/linemarks) draws a
// VSCode-style change bar in the gutter when the file is git-modified.
const SCALE = 0.16; // minimap size relative to the real code

// Above this many lines the minimap is suppressed: it mirrors the file's text a second
// time, so a huge file would double an already-large node count (heavy to mount and to
// repaint) for a mirror that's illegible at 0.16 scale anyway. The code area still
// scrolls normally without it.
const MINIMAP_MAX_LINES = 10000;

// Per-line change marks for the gutter change bar.
export interface LineMarks {
  added?: number[];
  modified?: number[];
  deleted?: number[];
}

// highlight.js emits one HTML string with `\n` between logical lines and (possibly
// multi-line) <span> tokens. To render one grid row per logical line — so line numbers
// stay aligned even when a line soft-wraps — split that string at each newline, closing
// any open <span>s at the break and reopening them on the next line. Returns one HTML
// fragment per logical line.
function splitHighlightedLines(html: string): string[] {
  const lines: string[] = [];
  const open: string[] = []; // stack of currently-open <span ...> opening tags
  let cur = "";
  let i = 0;
  while (i < html.length) {
    const ch = html[i];
    if (ch === "<") {
      const end = html.indexOf(">", i);
      if (end === -1) {
        cur += html.slice(i);
        break;
      }
      const tag = html.slice(i, end + 1);
      if (tag[1] === "/") open.pop();
      else open.push(tag); // hljs never self-closes spans
      cur += tag;
      i = end + 1;
    } else if (ch === "\n") {
      for (let k = 0; k < open.length; k++) cur += "</span>";
      lines.push(cur);
      cur = open.join(""); // reopen the same spans on the next line
      i++;
    } else {
      cur += ch;
      i++;
    }
  }
  for (let k = 0; k < open.length; k++) cur += "</span>";
  lines.push(cur);
  return lines;
}

interface CodeViewProps {
  html: string;
  lines: number;
  lineNumbers?: boolean;
  wrap?: boolean;
  minimap?: boolean;
  marks?: LineMarks | null;
  targetLine?: number;
  targetColumn?: number;
  /** Scroll-position memory (parts/useScrollMemory). The element that scrolls is .codeview, so
   *  this is merged with the inner ref before being attached. The callback is expected to have a
   *  stable identity per pane. */
  scrollMemory?: ScrollMemoryRef;
}

// Memoised: FileView re-renders on every text-selection change (to position the send /
// read-aloud pill), but the grid's props (html / lines / gutter / wrap / marks) are stable
// across those renders. Without memo, each Shift+↓ rebuilt all N line rows and forced the
// browser to re-evaluate the live selection inside the contentEditable — so holding the
// key got progressively slower. memo skips the grid entirely when only `sel` changed.
export const CodeView = memo(function CodeView({ html, lines, lineNumbers, wrap, minimap, marks, targetLine, scrollMemory }: CodeViewProps) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const miniInnerRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  const [vpVisible, setVpVisible] = useState(false); // toggles rarely — NOT per scroll

  // Big files: don't render the second (mirrored) DOM at all.
  const showMini = !!minimap && lines <= MINIMAP_MAX_LINES;

  // The minimap mirrors the code as PLAIN TEXT, not the highlighted markup: at 0.16
  // scale the token colours are unreadable anyway, but the thousands of hljs <span>s
  // were the dominant cost — every text-selection in the code area triggers a
  // document-wide style recalc + repaint that had to walk the scaled mirror's spans
  // each frame, which is what froze the browser on a wide selection (and doubled the
  // node count at open). Stripping to plain text keeps the shape map for pennies.
  const miniText = useMemo(() => {
    if (!showMini) return "";
    return html
      .replace(/<[^>]+>/g, "")
      .replace(/&lt;/g, "<")
      .replace(/&gt;/g, ">")
      .replace(/&amp;/g, "&");
  }, [html, showMini]);

  // Defer the minimap's first paint until the browser is idle, so opening a big file
  // renders the code grid immediately instead of blocking on the mirror. Reset on each
  // new file so it defers again.
  const [miniReady, setMiniReady] = useState(false);
  useEffect(() => {
    if (!showMini) return;
    setMiniReady(false);
    let idle = 0;
    let timer: ReturnType<typeof setTimeout> | null = null;
    if (typeof requestIdleCallback === "function") {
      idle = requestIdleCallback(() => setMiniReady(true), { timeout: 400 });
    } else {
      timer = setTimeout(() => setMiniReady(true), 120);
    }
    return () => {
      if (idle) cancelIdleCallback(idle);
      if (timer) clearTimeout(timer);
    };
  }, [showMini, html]);

  // Cached geometry, updated only when the layout can actually change (content / wrap /
  // gutter / resize) — never on scroll. Keeping the scroll hot path free of layout
  // reads (offsetHeight) is what stops the scroll-time layout thrash that made big
  // files janky. offsetRef mirrors the current translate so the click/drag handlers can
  // map a cursor Y back to a scroll position without reading React state.
  const mirrorH = useRef(0); // mirror's natural (pre-scale) height in px
  const offsetRef = useRef(0);
  const rafRef = useRef(0);

  const measure = useCallback(() => {
    mirrorH.current = miniInnerRef.current ? miniInnerRef.current.offsetHeight : 0;
  }, []);

  // Position the mirror + viewport box for the current scrollTop, writing styles
  // imperatively (no setState) so a scroll doesn't re-render React. Runs inside a rAF,
  // at most once per frame. Uses the cached mirror height, so there's no forced reflow.
  const apply = useCallback(() => {
    const sc = scrollRef.current;
    const inner = miniInnerRef.current;
    if (!sc || !inner) return;
    const contentH = sc.scrollHeight;
    const paneViewH = sc.clientHeight;
    if (contentH <= paneViewH + 1) {
      setVpVisible((v) => (v ? false : v));
      offsetRef.current = 0;
      inner.style.transform = `translateY(0px) scale(${SCALE})`;
      return;
    }
    setVpVisible((v) => (v ? v : true));
    const visualH = mirrorH.current * SCALE; // scaled mirror height
    const ratio = visualH / contentH;
    const vpH = paneViewH * ratio;
    let offset = 0;
    if (visualH > paneViewH) offset = (sc.scrollTop / (contentH - paneViewH)) * (visualH - paneViewH);
    offsetRef.current = offset;
    inner.style.transform = `translateY(${-offset}px) scale(${SCALE})`;
    const box = viewportRef.current;
    if (box) {
      box.style.top = sc.scrollTop * ratio - offset + "px";
      box.style.height = vpH + "px";
    }
  }, []);

  // Coalesce scroll/resize bursts into one update per animation frame.
  const schedule = useCallback(() => {
    if (rafRef.current) return;
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = 0;
      apply();
    });
  }, [apply]);

  useLayoutEffect(() => {
    if (!showMini || !miniReady) return;
    measure();
    apply();
  }, [html, lineNumbers, wrap, showMini, miniReady, measure, apply]);

  // The viewport box is mounted only once vpVisible flips true, so the apply() that
  // flipped it couldn't position it yet (its ref wasn't attached). Re-apply once it's
  // in the DOM so it lands in the right place without waiting for the next scroll.
  useLayoutEffect(() => {
    if (vpVisible) apply();
  }, [vpVisible, apply]);

  useEffect(() => {
    const sc = scrollRef.current;
    if (!sc || !showMini) return;
    const onScroll = () => schedule();
    const onResize = () => {
      measure();
      schedule();
    };
    sc.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onResize);
    return () => {
      sc.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onResize);
      if (rafRef.current) {
        cancelAnimationFrame(rafRef.current);
        rafRef.current = 0;
      }
    };
  }, [schedule, measure, showMini]);

  // Map a Y within the minimap pane to a scroll position (centred on the cursor).
  const scrollToMini = (clientY: number, paneEl: HTMLElement) => {
    const sc = scrollRef.current;
    if (!sc) return;
    const rect = paneEl.getBoundingClientRect();
    const yInPane = clientY - rect.top;
    const visualPos = yInPane + offsetRef.current; // position within the scaled mirror (0..visualH)
    // Map the scaled-mirror position back to REAL content coordinates using the same
    // visualH/contentH ratio the viewport box uses in apply(). With wrap on, the mirror
    // (unwrapped) is shorter than the wrapped content, so the old `/SCALE` (mirror-natural
    // height) undershot and the drag lagged; this ratio stays consistent with the box and
    // reduces to `/SCALE` when unwrapped (contentH == mirror's natural height).
    const visualH = mirrorH.current * SCALE;
    const contentH = sc.scrollHeight;
    const main = visualH > 0 ? (visualPos / visualH) * contentH : visualPos / SCALE;
    sc.scrollTop = main - sc.clientHeight / 2;
  };

  const onMiniDown = (e: RMouseEvent) => {
    const pane = e.currentTarget as HTMLElement;
    scrollToMini(e.clientY, pane);
    const move = (ev: MouseEvent) => scrollToMini(ev.clientY, pane);
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  // Touch: tap or swipe up/down on the minimap to scroll. `touch-action: none` on
  // .minimap (styles.css) hands us the gesture so the page doesn't scroll instead.
  const onMiniTouch = (e: RTouchEvent) => {
    const t = e.touches[0];
    if (t) scrollToMini(t.clientY, e.currentTarget as HTMLElement);
  };

  // Per-line change classes for the gutter change bar. Now that each logical line is
  // its own grid row, the bar aligns with soft-wrapped lines too, so it no longer has
  // to be suppressed when wrap is on.
  const changes = useMemo(() => {
    if (!marks) return null;
    const cls: Record<number, string> = {};
    for (const n of marks.added || []) cls[n] = "cb-add";
    for (const n of marks.modified || []) cls[n] = "cb-mod";
    const del = new Set(marks.deleted || []);
    if (!Object.keys(cls).length && !del.size) return null;
    return { cls, del };
  }, [marks]);

  const showGutter = lineNumbers || changes;
  const n = Math.max(lines, 1);
  const lineHtmls = useMemo(() => splitHighlightedLines(html), [html]);

  // Source citations open with the referenced row centered. useLayoutEffect runs after
  // the line grid exists but before paint, avoiding a visible jump from the first line.
  useLayoutEffect(() => {
    if (!targetLine) return;
    scrollRef.current
      ?.querySelector<HTMLElement>(`.cl-code[data-ln="${targetLine}"]`)
      ?.closest<HTMLElement>(".cl-row")
      ?.scrollIntoView({ block: "center" });
  }, [html, targetLine]);

  // The code area is contentEditable so it gets a real, keyboard-movable caret (arrow
  // keys, Home/End, Shift-select, Ctrl+A, copy) for reading — but it must stay strictly
  // read-only, so every mutation path is blocked: beforeinput covers typing/delete/IME,
  // and paste/cut/drop are cancelled explicitly. Caret movement, selection and copy don't
  // fire these, so they pass through. The line-number gutter is a contentEditable=false
  // island so the caret never lands on a number.
  const preventEdit = useCallback((e: SyntheticEvent) => e.preventDefault(), []);

  // Merges the inner ref (used for the minimap maths) and the scroll memory passed from outside
  // into one ref. The memory side returns a React 19 ref cleanup, so that is folded in too.
  const attachScroll = useCallback(
    (el: HTMLDivElement) => {
      scrollRef.current = el;
      const detach = scrollMemory?.(el);
      return () => {
        scrollRef.current = null;
        detach?.();
      };
    },
    [scrollMemory],
  );

  return (
    <div className="codeview-wrap">
      <div className={"codeview" + (wrap ? " wrap" : "")} ref={attachScroll}>
        <div
          className={"codegrid" + (showGutter ? "" : " no-gutter")}
          contentEditable
          suppressContentEditableWarning
          spellCheck={false}
          tabIndex={0}
          role="textbox"
          aria-readonly="true"
          aria-multiline="true"
          onBeforeInput={preventEdit}
          onPaste={preventEdit}
          onCut={preventEdit}
          onDrop={preventEdit}
        >
          {Array.from({ length: n }, (_, i) => {
            const ln = i + 1;
            let gutCls = "";
            if (changes) {
              if (changes.cls[ln]) gutCls += " " + changes.cls[ln];
              if (changes.del.has(ln)) gutCls += " cb-del";
            }
            return (
              <div className={"cl-row" + (ln === targetLine ? " target-line" : "")} key={i}>
                {showGutter && (
                  <div className={"cl-gutter" + gutCls} contentEditable={false} aria-hidden="true">
                    {lineNumbers ? ln : ""}
                  </div>
                )}
                <div className="cl-code hljs" data-ln={ln} dangerouslySetInnerHTML={{ __html: lineHtmls[i] ?? "" }} />
              </div>
            );
          })}
        </div>
      </div>
      {showMini && (
        <div className="minimap" onMouseDown={onMiniDown} onTouchStart={onMiniTouch} onTouchMove={onMiniTouch}>
          <div className="minimap-inner" ref={miniInnerRef} style={{ transform: `scale(${SCALE})` }}>
            {miniReady && <pre className="code hljs">{miniText}</pre>}
          </div>
          {vpVisible && miniReady && <div className="minimap-viewport" ref={viewportRef} />}
        </div>
      )}
    </div>
  );
});
