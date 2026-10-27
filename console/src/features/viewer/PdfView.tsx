// PdfView — reads a PDF inside a pane (docs/log/82).
//
// Rendering is pdf.js, serialisation is the render queue below, and zoom is stepped with
// "fit width" as 1. Pages scroll continuously rather than one at a time: the pane is tall and
// narrow, so scrolling beats page buttons. Canvases of pages far from the viewport are dropped
// to give their area back.
//
// Text is selectable, copyable and findable with the browser's Ctrl+F through pdf.js's
// TextLayer: transparent spans laid over each page at the glyph positions, the same mechanism as
// the stock pdf.js viewer. Unlike the canvas it is kept for every page, or find would only see
// the pages near the viewport.
//
// With focus in the pane, Ctrl/Cmd+F opens the pane's own find bar instead (pdfFind.ts): the
// browser's find loses its place as text layers are built under it, and cannot be tested.
//
// The bytes are fetched by pdf.js itself from the download endpoint (via `url`). That path
// supports Range, so a large PDF starts rendering without loading the whole file into JS memory
// (the Agent serves with http.ServeContent and the CP proxy relays the headers unchanged).
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as RKeyboardEvent,
} from "react";
import type { PDFDocumentLoadingTask, PDFDocumentProxy, RenderTask, TextLayer } from "pdfjs-dist";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { documentAssetParams, loadPdfjs, type Pdfjs } from "./pdfjs.ts";
import { clipboardText } from "./pdfText.ts";
import { findMatches, firstMatchFrom, pageText, stepMatch, type FindMatch, type PageText } from "./pdfFind.ts";
import { setFindHighlights } from "./pdfFindHighlight.ts";
import {
  anchorOf,
  canvasPixelRatio,
  currentPage,
  fitScale,
  layoutPages,
  PAGE_GAP,
  scrollTopForAnchor,
  stepZoom,
  visibleRange,
  type PageSize,
  type ScrollAnchor,
} from "./pdfPages.ts";
import type { ScrollMemoryRef } from "./parts/useScrollMemory.ts";

/** Horizontal padding around a page; must match the padding of `.pdfview-doc`. */
const PAGE_PAD = 12;
/** Pages far from the viewport drop their canvas; this is how many pages to keep either side. */
const KEEP = 2;

interface PdfViewProps {
  /** URL of the raw bytes (the download endpoint). */
  src: string;
  /** Reports the page count to the parent once opened, for the info bar. */
  onMeta?: (meta: { pages: number }) => void;
  /** Scroll memory (parts/useScrollMemory); returns to the same page after a tab switch. */
  scrollMemory?: ScrollMemoryRef;
}

/** Kind of failure to surface, derived from the pdf.js exception name. */
type Failure = "" | "password" | "broken" | "load";

function failureOf(e: unknown): Failure {
  const name = (e as { name?: string } | null)?.name || "";
  if (name === "PasswordException") return "password";
  if (name === "InvalidPDFException") return "broken";
  return "load";
}

/** Empties a page's text layer. Detached layers are fine: cancel() on a finished one is a no-op. */
function dropTextLayer(layers: Map<number, TextLayer>, i: number, div: HTMLDivElement | undefined) {
  layers.get(i)?.cancel();
  layers.delete(i);
  div?.replaceChildren();
}

export function PdfView({ src, onMeta, scrollMemory }: PdfViewProps) {
  const tr = useT();
  const scrollRef = useRef<HTMLDivElement>(null);
  const canvasesRef = useRef(new Map<number, HTMLCanvasElement>());
  // Which page was rendered at which scale. A scale change makes every page due for a redraw.
  const renderedRef = useRef(new Map<number, number>());
  // Text layer container per page, and the finished TextLayer filling it. A zoom updates the
  // existing layer in place instead of rebuilding it, so the spans keep their identity.
  const textDivsRef = useRef(new Map<number, HTMLDivElement>());
  const textLayersRef = useRef(new Map<number, TextLayer>());
  const docRef = useRef<PDFDocumentProxy | null>(null);
  // Every page's searchable text, extracted once per document when the find bar first searches.
  const pageTextsRef = useRef<Promise<PageText[]> | null>(null);
  // Bumped when a new document starts loading. Async work for the previous one checks it after
  // every await: page indices repeat across documents, so a late job would otherwise write into,
  // or tear down, the new document's page of the same number.
  const docGenRef = useRef(0);
  // The text layer being built right now, so a document switch can cancel it mid-render.
  const buildingRef = useRef<TextLayer | null>(null);
  const pdfjsRef = useRef<Pdfjs | null>(null);
  const jobRef = useRef<{ cancelled: boolean; task: RenderTask | null } | null>(null);
  const anchorRef = useRef<ScrollAnchor | null>(null);
  const onMetaRef = useRef(onMeta);
  onMetaRef.current = onMeta;

  // Canvas registration. Never rebuild this function per render: when a ref callback's identity
  // changes, React detaches the old one and attaches a new one every time, so the detach cleanup
  // (discarding the rendered record) runs for every page on every scroll. Measured: a finished
  // page immediately reverted to "not rendered" and every page was redrawn on each scroll
  // (caught by the scripts/pdf/check.mjs check "off-screen pages release their canvas").
  const attachCanvas = useCallback((el: HTMLCanvasElement) => {
    const i = Number(el.dataset.page);
    canvasesRef.current.set(i, el);
    return () => {
      if (canvasesRef.current.get(i) === el) canvasesRef.current.delete(i);
      renderedRef.current.delete(i);
    };
  }, []);

  // Same identity rule as attachCanvas.
  const attachText = useCallback((el: HTMLDivElement) => {
    const i = Number(el.dataset.page);
    textDivsRef.current.set(i, el);
    return () => {
      if (textDivsRef.current.get(i) === el) textDivsRef.current.delete(i);
      dropTextLayer(textLayersRef.current, i, el);
      renderedRef.current.delete(i);
    };
  }, []);

  // Bundles the inner ref and the scroll memory passed in into one (same shape as CodeView).
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

  const [pageSizes, setPageSizes] = useState<PageSize[]>([]);
  const [failure, setFailure] = useState<Failure>("");
  const [zoom, setZoom] = useState(1);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [scrollTop, setScrollTop] = useState(0);
  // Version counter telling the render side that the document was swapped. The
  // PDFDocumentProxy itself is not state: it never changes, so it cannot be a redraw dependency.
  const [docEpoch, setDocEpoch] = useState(0);
  // Bumped whenever a page's text layer is built, so find can paint matches that just got spans.
  const [layerTick, setLayerTick] = useState(0);
  const [findOpen, setFindOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState<FindMatch[]>([]);
  const [active, setActive] = useState(-1);
  // Bumped to scroll the active match into view; a counter, because stepping through a single
  // match leaves `active` unchanged and must still bring it back.
  const [reveal, setReveal] = useState(0);
  const findInputRef = useRef<HTMLInputElement>(null);

  // --- Loading ---------------------------------------------------------------
  useEffect(() => {
    let alive = true;
    // Clean up the loading task, not the document: pdf.js 6 removed destroy() from
    // PDFDocumentProxy, and only the task can shut the worker and its channel down.
    let opened: PDFDocumentLoadingTask | null = null;
    setPageSizes([]);
    setFailure("");
    setZoom(1);
    setScrollTop(0);
    renderedRef.current.clear();
    canvasesRef.current.clear();
    for (const [i, layer] of textLayersRef.current) {
      layer.cancel();
      textDivsRef.current.get(i)?.replaceChildren();
    }
    textLayersRef.current.clear();
    docGenRef.current++;
    buildingRef.current?.cancel();
    buildingRef.current = null;
    docRef.current = null;
    pageTextsRef.current = null;
    setMatches([]);
    setActive(-1);
    const run = async () => {
      const pdfjs = await loadPdfjs();
      if (!alive) return;
      pdfjsRef.current = pdfjs;
      const task = pdfjs.getDocument({ url: src, ...documentAssetParams() });
      opened = task;
      const doc = await task.promise;
      if (!alive) {
        void task.destroy();
        return;
      }
      // Collect every page's intrinsic size up front. Without it the scroll height is unknown,
      // the scrollbar grows and shrinks while loading and the reading position jumps.
      const sizes: PageSize[] = [];
      for (let n = 1; n <= doc.numPages; n++) {
        const page = await doc.getPage(n);
        if (!alive) return;
        const vp = page.getViewport({ scale: 1 });
        sizes.push({ w: vp.width, h: vp.height });
      }
      if (!alive) return;
      docRef.current = doc;
      setPageSizes(sizes);
      setDocEpoch((n) => n + 1);
      onMetaRef.current?.({ pages: doc.numPages });
    };
    run().catch((e) => {
      if (alive) setFailure(failureOf(e));
    });
    return () => {
      alive = false;
      if (jobRef.current) jobRef.current.cancelled = true;
      void opened?.destroy();
      docRef.current = null;
    };
  }, [src]);

  // --- Container size and scroll position -------------------------------------
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const measure = () => setBox({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    let raf = 0;
    const onScroll = () => {
      if (raf) return;
      raf = requestAnimationFrame(() => {
        raf = 0;
        setScrollTop(el.scrollTop);
      });
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("scroll", onScroll);
      if (raf) cancelAnimationFrame(raf);
    };
  }, []);

  const base = useMemo(() => fitScale(pageSizes, box.w, PAGE_PAD), [pageSizes, box.w]);
  const scale = base * zoom;
  const layout = useMemo(() => layoutPages(pageSizes, scale), [pageSizes, scale]);
  const range = useMemo(() => visibleRange(layout, scrollTop, box.h), [layout, scrollTop, box.h]);
  const pageNo = currentPage(layout, scrollTop, box.h);

  // --- Preserve the reading position, but only across a zoom change ------------
  // Changing the scale changes every page height, so keeping the raw scroll position lands on a
  // completely different page. Remember it as "page plus fraction within that page" before the
  // change and restore it as soon as the new layout is settled, before paint.
  const rememberAnchor = useCallback(() => {
    anchorRef.current = anchorOf(layout, scrollRef.current?.scrollTop ?? 0);
  }, [layout]);

  useLayoutEffect(() => {
    const el = scrollRef.current;
    const anchor = anchorRef.current;
    if (!el || !anchor) return;
    anchorRef.current = null;
    el.scrollTop = scrollTopForAnchor(layout, anchor);
    setScrollTop(el.scrollTop);
  }, [layout]);

  // --- Rendering, one page at a time, serially --------------------------------
  useEffect(() => {
    const doc = docRef.current;
    if (!doc || !(scale > 0) || range.end <= range.start) return;
    // Throw away the render in flight: waiting for a render at the previous scale leaves the
    // page blurry for seconds after the gesture ends. pdf.js cancel rejects, so swallow it.
    if (jobRef.current) {
      jobRef.current.cancelled = true;
      jobRef.current.task?.cancel();
    }
    const job: { cancelled: boolean; task: RenderTask | null } = { cancelled: false, task: null };
    jobRef.current = job;

    const pdfjs = pdfjsRef.current;
    const run = async () => {
      if (!pdfjs) return;
      for (let i = range.start; i < range.end; i++) {
        if (job.cancelled) return;
        if (renderedRef.current.get(i) === scale) continue;
        const canvas = canvasesRef.current.get(i);
        if (!canvas) continue;
        const page = await doc.getPage(i + 1);
        if (job.cancelled) return;
        const css = layout.sizes[i];
        const ratio = canvasPixelRatio(css.w, css.h, window.devicePixelRatio || 1);
        const viewport = page.getViewport({ scale: scale * ratio });
        canvas.width = Math.max(1, Math.floor(viewport.width));
        canvas.height = Math.max(1, Math.floor(viewport.height));
        const ctx = canvas.getContext("2d");
        if (!ctx) return;
        const task = page.render({ canvasContext: ctx, viewport, canvas });
        job.task = task;
        try {
          await task.promise;
        } catch {
          return; // cancel or a render failure; either way just end this pass
        }
        job.task = null;
        if (job.cancelled) return;
        // The text layer is laid out in CSS pixels (the canvas ratio above is only bitmap
        // density); its font sizes follow --total-scale-factor on the page element. Building it
        // is the text effect's job; here an existing one is only refitted to the new scale.
        const textViewport = page.getViewport({ scale });
        textLayersRef.current.get(i)?.update({ viewport: textViewport });
        renderedRef.current.set(i, scale);
      }
    };
    void run();
    return () => {
      job.cancelled = true;
      job.task?.cancel();
    };
  }, [docEpoch, scale, range.start, range.end, layout]);

  // Canvases of pages far from the viewport give their area back. Scroll a long document to the
  // end once and keeping them would pile up one bitmap per page in the tab.
  useEffect(() => {
    for (const [i, canvas] of canvasesRef.current) {
      if (i >= range.start - KEEP && i < range.end + KEEP) continue;
      if (!renderedRef.current.has(i)) continue;
      canvas.width = 0;
      canvas.height = 0;
      renderedRef.current.delete(i);
    }
  }, [range.start, range.end]);

  // --- Text layers for every page ----------------------------------------------
  // The browser's own find (Ctrl+F) searches only text that is in the DOM, so a layer that lived
  // and died with its canvas made find report "1/1" and Enter never reached another page. Every
  // page gets one, nearest to the viewport first, and keeps it when its canvas is released. A
  // layer sizes itself from --total-scale-factor, so one built at an earlier zoom still lines up;
  // the render pass refits the visible ones.
  const rangeRef = useRef(range);
  rangeRef.current = range;
  const scaleRef = useRef(scale);
  scaleRef.current = scale;
  useEffect(() => {
    const doc = docRef.current;
    const pdfjs = pdfjsRef.current;
    if (!doc || !pdfjs) return;
    let cancelled = false;
    const gen = docGenRef.current;
    const stale = () => cancelled || gen !== docGenRef.current;
    // A page whose text cannot be read is skipped for this document, or the loop would retry it
    // forever.
    const failed = new Set<number>();
    const next = () => {
      const from = rangeRef.current.start;
      let best = -1;
      for (let i = 0; i < doc.numPages; i++) {
        if (textLayersRef.current.has(i) || failed.has(i) || !textDivsRef.current.has(i)) continue;
        if (best < 0 || Math.abs(i - from) < Math.abs(best - from)) best = i;
      }
      return best;
    };
    // Tears down only what this job built: the map entry for page i may already belong to a
    // newer document, so it is left alone unless it is this very layer.
    const discard = (i: number, layer: TextLayer, div: HTMLDivElement) => {
      layer.cancel();
      if (textLayersRef.current.get(i) === layer) textLayersRef.current.delete(i);
      if (textDivsRef.current.get(i) !== div || !textLayersRef.current.has(i)) div.replaceChildren();
    };
    const run = async () => {
      for (let i = next(); i >= 0 && !stale(); i = next()) {
        const page = await doc.getPage(i + 1);
        const textDiv = textDivsRef.current.get(i);
        if (stale() || !textDiv || !(scaleRef.current > 0)) return;
        const layer = new pdfjs.TextLayer({
          textContentSource: page.streamTextContent({ includeMarkedContent: true, disableNormalization: true }),
          container: textDiv,
          viewport: page.getViewport({ scale: scaleRef.current }),
        });
        buildingRef.current = layer;
        let ok = true;
        try {
          await layer.render();
        } catch {
          ok = false;
        }
        if (buildingRef.current === layer) buildingRef.current = null;
        if (stale()) {
          discard(i, layer, textDiv);
          return;
        }
        if (!ok) {
          discard(i, layer, textDiv);
          failed.add(i);
          continue;
        }
        // Without this block, dragging across a gap between lines jumps the selection to the
        // end of the layer; `.selecting` stretches it over the whole page (see pdf.css).
        const end = document.createElement("div");
        end.className = "endOfContent";
        textDiv.append(end);
        textLayersRef.current.set(i, layer);
        setLayerTick((n) => n + 1);
      }
    };
    // A rejection here is the document going away under getPage; there is nothing to report.
    run().catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [docEpoch]);

  // Selection support, mirroring pdf.js's TextLayerBuilder: `.selecting` while the pointer is
  // down, and a copy handler that normalises what goes to the clipboard (pdfText.ts).
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const onDown = (e: MouseEvent) => {
      (e.target as Element | null)?.closest(".textLayer")?.classList.add("selecting");
    };
    const onUp = () => {
      for (const div of textDivsRef.current.values()) div.classList.remove("selecting");
    };
    const onCopy = (e: ClipboardEvent) => {
      const selection = document.getSelection();
      if (!selection || selection.isCollapsed || !e.clipboardData) return;
      const pdfjs = pdfjsRef.current;
      if (!pdfjs) return;
      e.clipboardData.setData("text/plain", clipboardText(selection.toString(), pdfjs.normalizeUnicode));
      e.preventDefault();
    };
    el.addEventListener("mousedown", onDown);
    el.addEventListener("copy", onCopy);
    document.addEventListener("pointerup", onUp);
    window.addEventListener("blur", onUp);
    return () => {
      el.removeEventListener("mousedown", onDown);
      el.removeEventListener("copy", onCopy);
      document.removeEventListener("pointerup", onUp);
      window.removeEventListener("blur", onUp);
    };
  }, []);

  // --- Find ---------------------------------------------------------------------
  const pageNoRef = useRef(1);
  pageNoRef.current = pageNo;

  const pageTexts = useCallback((): Promise<PageText[]> => {
    const doc = docRef.current;
    if (!doc) return Promise.resolve([]);
    pageTextsRef.current ??= (async () => {
      const out: PageText[] = [];
      for (let n = 1; n <= doc.numPages; n++) {
        // One unreadable page searches as empty rather than failing every other page with it
        // (and a rejected promise would stay cached for the document's lifetime).
        try {
          const page = await doc.getPage(n);
          // The same options as the text layer, and the same filter as TextLayer (items carrying
          // `str`), so item k here is span k there.
          const content = await page.getTextContent({ includeMarkedContent: true, disableNormalization: true });
          out.push(pageText(content.items.flatMap((it) => ("str" in it ? [it.str] : []))));
        } catch {
          out.push(pageText([]));
        }
      }
      return out;
    })();
    return pageTextsRef.current;
  }, []);

  useEffect(() => {
    if (!findOpen || !query.trim()) {
      setMatches([]);
      setActive(-1);
      return;
    }
    let alive = true;
    const pending = pageTexts();
    pending.then(
      (texts) => {
        // A document switch clears pageTextsRef before this effect re-runs; results from the
        // previous document must not land on the new one.
        if (!alive || pageTextsRef.current !== pending) return;
        const found = findMatches(texts, query);
        setMatches(found);
        setActive(firstMatchFrom(found, pageNoRef.current - 1));
        setReveal((n) => n + 1);
      },
      () => {
        if (alive) setMatches([]);
      },
    );
    return () => {
      alive = false;
    };
  }, [findOpen, query, docEpoch, pageTexts]);

  // A match as a DOM range over its page's text-layer spans; null while that page has no layer.
  const rangeOf = useCallback((m: FindMatch): Range | null => {
    const layer = textLayersRef.current.get(m.page);
    const a = layer?.textDivs[m.startItem]?.firstChild;
    const b = layer?.textDivs[m.endItem]?.firstChild;
    if (!a || !b) return null;
    try {
      const r = document.createRange();
      r.setStart(a, m.startOffset);
      r.setEnd(b, m.endOffset);
      return r;
    } catch {
      return null; // the layer holds different text than was searched; leave it unpainted
    }
  }, []);

  // Paints the matches and, when asked, brings the active one into view. A match on a page with
  // no text layer yet is reached by scrolling to its page, once per request so the reader can
  // scroll away if that page never gets a layer; the layer built there bumps layerTick and this
  // runs again to place it exactly. Ranges are cached per match list, so a layer being built
  // only computes the matches still missing one.
  const revealedRef = useRef(0);
  const pagedRef = useRef(0);
  const rangesRef = useRef<{ of: FindMatch[]; ranges: (Range | null)[] }>({ of: [], ranges: [] });
  useEffect(() => {
    const owner = scrollRef;
    if (rangesRef.current.of !== matches) rangesRef.current = { of: matches, ranges: matches.map(() => null) };
    const { ranges } = rangesRef.current;
    const all: Range[] = [];
    matches.forEach((m, k) => {
      ranges[k] ??= rangeOf(m);
      const r = ranges[k];
      if (r) all.push(r);
    });
    const current = ranges[active] ?? null;
    setFindHighlights(owner, all, current);
    const el = scrollRef.current;
    const m = matches[active];
    if (!el || !m || revealedRef.current === reveal) return;
    if (!current) {
      if (pagedRef.current !== reveal && layout.tops[m.page] !== undefined) {
        pagedRef.current = reveal;
        el.scrollTop = layout.tops[m.page];
        setScrollTop(el.scrollTop);
      }
      return;
    }
    revealedRef.current = reveal;
    const r = current.getBoundingClientRect();
    const view = el.getBoundingClientRect();
    if (r.top < view.top || r.bottom > view.bottom) el.scrollTop += r.top - view.top - el.clientHeight / 3;
    if (r.left < view.left || r.right > view.right) el.scrollLeft += r.left - view.left - el.clientWidth / 3;
    setScrollTop(el.scrollTop);
  }, [matches, active, reveal, layerTick, layout, rangeOf]);

  useEffect(() => {
    const owner = scrollRef;
    return () => setFindHighlights(owner, [], null);
  }, []);

  const openFind = () => {
    setFindOpen(true);
    // The bar mounts on this render; focus it once it is there.
    requestAnimationFrame(() => {
      findInputRef.current?.focus();
      findInputRef.current?.select();
    });
  };

  const closeFind = () => {
    setFindOpen(false);
    scrollRef.current?.focus();
  };

  const stepFind = (dir: 1 | -1) => {
    if (matches.length === 0) return;
    setActive((i) => stepMatch(i, matches.length, dir));
    setReveal((n) => n + 1);
  };

  // Ctrl/Cmd+F anywhere in the pane, the find bar included, opens (or re-selects) the pane's
  // find instead of the browser's.
  const onPaneKeyDown = (e: RKeyboardEvent<HTMLDivElement>) => {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && (e.key === "f" || e.key === "F")) {
      e.preventDefault();
      openFind();
    }
  };

  const onFindKeyDown = (e: RKeyboardEvent<HTMLInputElement>) => {
    // Enter that confirms an IME conversion is not a request for the next match.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter") {
      e.preventDefault();
      stepFind(e.shiftKey ? -1 : 1);
    }
  };

  // Escape anywhere in the bar (the input or a button) closes it and stays inside the pane.
  const onBarKeyDown = (e: RKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Escape" || e.nativeEvent.isComposing || e.keyCode === 229) return;
    e.preventDefault();
    e.stopPropagation();
    closeFind();
  };

  // --- Controls ---------------------------------------------------------------
  const applyZoom = useCallback(
    (next: number) => {
      rememberAnchor();
      setZoom(next);
      renderedRef.current.clear();
    },
    [rememberAnchor],
  );

  // Ctrl/Cmd + wheel zooms; a plain wheel stays scrolling, because this is a continuously
  // scrolled document and stealing the wheel for zoom stops the reader moving through it.
  // React's onWheel registers passive, where preventDefault does nothing, so bind natively
  // (same reason as ImageView).
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      applyZoom(Math.min(4, Math.max(0.25, zoom * Math.exp(-e.deltaY * 0.002))));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoom, applyZoom]);

  const goToPage = (n: number) => {
    const el = scrollRef.current;
    if (!el || layout.tops.length === 0) return;
    const i = Math.min(Math.max(0, n - 1), layout.tops.length - 1);
    el.scrollTop = layout.tops[i];
    setScrollTop(el.scrollTop);
  };

  if (failure) {
    return (
      <div className="pdfview is-failed">
        <p className="muted">
          {failure === "password"
            ? tr("view.pdf.password")
            : failure === "broken"
              ? tr("view.pdf.broken")
              : tr("view.pdf.cannot_load")}
        </p>
      </div>
    );
  }

  const pages = pageSizes.length;
  return (
    <div className="pdfview" onKeyDown={onPaneKeyDown}>
      {findOpen && (
        <div className="pdfview-find" role="search" onKeyDown={onBarKeyDown}>
          <input
            ref={findInputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onFindKeyDown}
            placeholder={tr("view.pdf.find")}
            aria-label={tr("view.pdf.find")}
            spellCheck={false}
          />
          <span className="pdfview-find-count" aria-live="polite">
            {query.trim() ? `${matches.length ? active + 1 : 0}/${matches.length}` : ""}
          </span>
          <button type="button" onClick={() => stepFind(-1)} disabled={matches.length === 0} title={tr("view.pdf.find_prev")}>
            <Icon name="chevron-up" />
          </button>
          <button type="button" onClick={() => stepFind(1)} disabled={matches.length === 0} title={tr("view.pdf.find_next")}>
            <Icon name="chevron-down" />
          </button>
          <button type="button" onClick={closeFind} title={tr("view.pdf.find_close")}>
            <Icon name="close" />
          </button>
        </div>
      )}
      <div className="pdfview-scroll" ref={attachScroll} tabIndex={0} aria-label={tr("view.pdf.aria")}>
        {pages === 0 ? (
          <p className="pdfview-loading muted">{tr("view.pdf.loading")}</p>
        ) : (
          <div className="pdfview-doc" style={{ padding: PAGE_PAD, gap: PAGE_GAP }}>
            {layout.sizes.map((size, i) => (
              <div
                className="pdfview-page"
                key={i}
                style={{ width: size.w, height: size.h, "--total-scale-factor": scale } as CSSProperties}
              >
                <canvas className="pdfview-canvas" data-page={i} style={{ width: size.w, height: size.h }} ref={attachCanvas} />
                <div className="textLayer" data-page={i} ref={attachText} />
              </div>
            ))}
          </div>
        )}
      </div>
      {pages > 0 && (
        <div className="pdfview-bar">
          <button type="button" onClick={() => goToPage(pageNo - 1)} disabled={pageNo <= 1} title={tr("view.pdf.prev_page")}>
            <Icon name="chevron-up" />
          </button>
          <span className="pdfview-pageno">{tr("view.pdf.page_of", { n: pageNo, total: pages })}</span>
          <button type="button" onClick={() => goToPage(pageNo + 1)} disabled={pageNo >= pages} title={tr("view.pdf.next_page")}>
            <Icon name="chevron-down" />
          </button>
          <span className="pdfview-sep" />
          <button type="button" className="pdfview-findbtn" onClick={openFind} title={tr("view.pdf.find")}>
            <Icon name="search" />
          </button>
          <button type="button" onClick={() => applyZoom(stepZoom(zoom, -1))} title={tr("view.pdf.zoom_out")}>
            <Icon name="dash" />
          </button>
          <button type="button" className="pdfview-zoomlabel" onClick={() => applyZoom(1)} title={tr("view.pdf.fit_width")}>
            {Math.round(zoom * 100)}%
          </button>
          <button type="button" onClick={() => applyZoom(stepZoom(zoom, 1))} title={tr("view.pdf.zoom_in")}>
            <Icon name="add" />
          </button>
        </div>
      )}
    </div>
  );
}
