// PdfView — reads a PDF inside a pane (docs/log/82).
//
// Rendering is pdf.js, serialisation is the render queue below, and zoom is stepped with
// "fit width" as 1. Pages scroll continuously rather than one at a time: the pane is tall and
// narrow, so scrolling beats page buttons. Canvases of pages far from the viewport are dropped
// to give their area back.
//
// The bytes are fetched by pdf.js itself from the download endpoint (via `url`). That path
// supports Range, so a large PDF starts rendering without loading the whole file into JS memory
// (the Agent serves with http.ServeContent and the CP proxy relays the headers unchanged).
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { PDFDocumentLoadingTask, PDFDocumentProxy, RenderTask } from "pdfjs-dist";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { documentAssetParams, loadPdfjs } from "./pdfjs.ts";
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

export function PdfView({ src, onMeta, scrollMemory }: PdfViewProps) {
  const tr = useT();
  const scrollRef = useRef<HTMLDivElement>(null);
  const canvasesRef = useRef(new Map<number, HTMLCanvasElement>());
  // Which page was rendered at which scale. A scale change makes every page due for a redraw.
  const renderedRef = useRef(new Map<number, number>());
  const docRef = useRef<PDFDocumentProxy | null>(null);
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
    docRef.current = null;
    const run = async () => {
      const pdfjs = await loadPdfjs();
      if (!alive) return;
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

    const run = async () => {
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
    <div className="pdfview">
      <div className="pdfview-scroll" ref={attachScroll} tabIndex={0} aria-label={tr("view.pdf.aria")}>
        {pages === 0 ? (
          <p className="pdfview-loading muted">{tr("view.pdf.loading")}</p>
        ) : (
          <div className="pdfview-doc" style={{ padding: PAGE_PAD, gap: PAGE_GAP }}>
            {layout.sizes.map((size, i) => (
              <div className="pdfview-page" key={i} style={{ width: size.w, height: size.h }}>
                <canvas className="pdfview-canvas" data-page={i} style={{ width: size.w, height: size.h }} ref={attachCanvas} />
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
