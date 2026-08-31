// PdfView — PDF をペインの中で読む面（docs/log/82）。
//
// codeleaf の `PdfViewer`（Android の PdfRenderer・同時 1 ページ・Mutex で直列化・
// −/＋ で 1〜3 倍）を Web に写したもの。描くのは pdf.js、直列化は下の描画キュー、
// 倍率は「幅に合わせる」を 1 とする段階ズーム。違いは 1 点だけで、こちらは 1 ページ
// ずつではなく縦に連続スクロールする（ペインは細長く、ページ送りボタンより
// スクロールの方が速い）。画面から遠いページの canvas は捨てて面積を戻す。
//
// バイト列は download エンドポイントから pdf.js 自身に取りに行かせる（url 指定）。
// Range が通る経路なので、大きな PDF でも全体を JS のメモリに載せずに読み始められる
// （Agent は http.ServeContent、CP のプロキシはヘッダをそのまま中継する）。
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

/** ページの左右に置く余白（.pdfview-doc の padding と一致させる）。 */
const PAGE_PAD = 12;
/** 画面から遠いページは canvas を捨てる。ここは「何ページ分残すか」。 */
const KEEP = 2;

interface PdfViewProps {
  /** 生バイトの URL（download エンドポイント）。 */
  src: string;
  /** 開けたときにページ数を親へ（情報バーの表示用）。 */
  onMeta?: (meta: { pages: number }) => void;
}

/** 表示に出す失敗の種類。pdf.js の例外名から引く。 */
type Failure = "" | "password" | "broken" | "load";

function failureOf(e: unknown): Failure {
  const name = (e as { name?: string } | null)?.name || "";
  if (name === "PasswordException") return "password";
  if (name === "InvalidPDFException") return "broken";
  return "load";
}

export function PdfView({ src, onMeta }: PdfViewProps) {
  const tr = useT();
  const scrollRef = useRef<HTMLDivElement>(null);
  const canvasesRef = useRef(new Map<number, HTMLCanvasElement>());
  // 何ページ目をどの倍率で描き終えたか。倍率が変われば全ページが描き直し対象になる。
  const renderedRef = useRef(new Map<number, number>());
  const docRef = useRef<PDFDocumentProxy | null>(null);
  const jobRef = useRef<{ cancelled: boolean; task: RenderTask | null } | null>(null);
  const anchorRef = useRef<ScrollAnchor | null>(null);
  const onMetaRef = useRef(onMeta);
  onMetaRef.current = onMeta;

  // canvas の登録。**この関数は毎レンダー作り直してはいけない**: ref コールバックの
  // 同一性が変わると、React は毎回「前のを外して新しいのを付け直す」ため、外した側の
  // 後始末（描画済み記録の破棄）が全ページぶん毎スクロール走る。実測では、描き終えた
  // ページが即座に「未描画」に戻り、スクロールのたび全ページを描き直していた
  // （scripts/pdf/check.mjs の「画面外のページは canvas を解放する」で発覚）。
  const attachCanvas = useCallback((el: HTMLCanvasElement) => {
    const i = Number(el.dataset.page);
    canvasesRef.current.set(i, el);
    return () => {
      if (canvasesRef.current.get(i) === el) canvasesRef.current.delete(i);
      renderedRef.current.delete(i);
    };
  }, []);

  const [pageSizes, setPageSizes] = useState<PageSize[]>([]);
  const [failure, setFailure] = useState<Failure>("");
  const [zoom, setZoom] = useState(1);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [scrollTop, setScrollTop] = useState(0);
  // 文書が入れ替わったことを描画側に伝えるための版番号（PDFDocumentProxy 自体は
  // state に置かない: 中身が変わらないオブジェクトなので再描画の依存にならない）。
  const [docEpoch, setDocEpoch] = useState(0);

  // --- 読み込み --------------------------------------------------------------
  useEffect(() => {
    let alive = true;
    // 後始末は「読み込みタスク」に対して行う。pdf.js 6 で PDFDocumentProxy から
    // destroy() が無くなり、ワーカーと通信を畳めるのはタスク側だけになった。
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
      // 各ページの素の大きさを先に集める。これが無いとスクロール高さが決まらず、
      // 読み込み中にスクロールバーが伸び縮みして読み位置が飛ぶ。
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

  // --- 容器の寸法とスクロール位置 --------------------------------------------
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

  // --- 倍率変更のときだけ読み位置を保つ --------------------------------------
  // 倍率を変えると全ページの高さが変わるので、スクロール位置をそのまま残すと
  // まったく別のページに飛ぶ。変更前に「ページ＋そのページ内の割合」で覚えておき、
  // 新しいレイアウトが確定した直後（ペイント前）に戻す。
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

  // --- 描画（1 枚ずつ直列に） ------------------------------------------------
  useEffect(() => {
    const doc = docRef.current;
    if (!doc || !(scale > 0) || range.end <= range.start) return;
    // 走っている描画は捨てる。倍率が変わったのに前の倍率の描画を待つと、指を離して
    // から数秒ぼやけたままになる（pdf.js の cancel は例外で返るので握り潰す）。
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
          return; // cancel か描画失敗。どちらもこの周回を畳むだけでよい
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

  // 画面から遠いページの canvas は面積を返す。長い文書を一度でも端まで送ると、
  // 残したままではページ数ぶんのビットマップがタブに積まれる。
  useEffect(() => {
    for (const [i, canvas] of canvasesRef.current) {
      if (i >= range.start - KEEP && i < range.end + KEEP) continue;
      if (!renderedRef.current.has(i)) continue;
      canvas.width = 0;
      canvas.height = 0;
      renderedRef.current.delete(i);
    }
  }, [range.start, range.end]);

  // --- 操作 ------------------------------------------------------------------
  const applyZoom = useCallback(
    (next: number) => {
      rememberAnchor();
      setZoom(next);
      renderedRef.current.clear();
    },
    [rememberAnchor],
  );

  // Ctrl/⌘＋ホイールで拡大縮小。素のホイールはスクロールのままにする（連続表示の
  // 読み物なので、拡大に取られると読み進められない）。React の onWheel は passive
  // 登録で preventDefault が効かないため、ネイティブで張る（ImageView と同じ理由）。
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
      <div className="pdfview-scroll" ref={scrollRef} tabIndex={0} aria-label={tr("view.pdf.aria")}>
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
