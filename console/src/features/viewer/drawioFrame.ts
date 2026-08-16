// drawio ビューアを閉じ込める iframe の中身（docs/65 §65.3・ADR 0046 決定 2）。
//
// なぜ iframe なのか（実測。docs/65 §65.2.1）:
//   - viewer-static.min.js は window にグローバルを 932 個生やし、`lang` / `dash` /
//     `Base64` / `MathJax` のような一般名や **window.DOMPurify の上書き**まで含む。
//     アプリの window に入れたら戻す手段が無い。
//   - ツールバーの lightbox は DRAWIO_LIGHTBOX_URL（既定 app.diagrams.net）を
//     window.open して図面を渡す。しかも showLightbox は
//     `"open" == lightbox || window.self !== window.top` を条件にしているので、
//     **iframe の中では設定に関わらず外部を開こうとする側に倒れる**。
//     ここでは (a) ツールバーに出さない (b) URL を空にする (c) 親が sandbox に
//     allow-popups を与えない、の三重で塞ぐ。
//
// srcdoc なので iframe はオリジンを持たない（親は allow-same-origin を与えない）。
// したがって図の XML は **親が取得して postMessage で渡す**。フレーム側は資格情報も
// 外向き通信も持たない（CSP connect-src 'none'）。

/** 親 → フレーム / フレーム → 親 のメッセージ。`af` で他の postMessage と混ざらないようにする。 */
export const DRAWIO_MSG = "af-drawio" as const;

export interface DrawioRenderRequest {
  af: typeof DRAWIO_MSG;
  t: "render";
  xml: string;
  dark: boolean;
}

export type DrawioFrameEvent =
  | { af: typeof DRAWIO_MSG; t: "ready" }
  | { af: typeof DRAWIO_MSG; t: "rendered"; pages: number; page: number; scale: number }
  | { af: typeof DRAWIO_MSG; t: "error"; code: "parse" | "empty" };

/** フレームから来たイベントか判定する（postMessage は誰でも送れる）。 */
export function isDrawioFrameEvent(data: unknown): data is DrawioFrameEvent {
  if (!data || typeof data !== "object") return false;
  const m = data as { af?: unknown; t?: unknown };
  return m.af === DRAWIO_MSG && (m.t === "ready" || m.t === "rendered" || m.t === "error");
}

// srcdoc は属性値なので、埋め込む文字列はダブルクォートまで含めて必ずエスケープする。
function attrEscape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * viewer の絶対 URL から script-src に載せるオリジンを得る。
 * srcdoc のオリジンは opaque なので 'self' では自分自身を指せず、
 * ビューアの実体があるオリジンを明示する必要がある。
 */
export function scriptOriginOf(viewerUrl: string): string {
  try {
    return new URL(viewerUrl).origin;
  } catch {
    return "";
  }
}

export interface DrawioFrameOptions {
  /** ビルド後のハッシュ付きアセット URL（絶対 URL であること）。 */
  viewerUrl: string;
  /** 初期テーマ。描画要求ごとにも渡すので、ここは最初の一枚の色を決めるだけ。 */
  dark: boolean;
}

/**
 * iframe に入れる HTML を組み立てる。
 *
 * CSP は default-src 'none' から始めて必要なものだけ開ける。**connect-src は 'none'**：
 * P0 ではステンシルを持たないので、フレームからの通信は 1 本も要らない
 * （ステンシルの遅延取得を入れる P1 で初めて開ける — docs/65 §65.5.3）。
 */
export function drawioFrameSrcdoc({ viewerUrl, dark }: DrawioFrameOptions): string {
  const origin = scriptOriginOf(viewerUrl);
  const csp = [
    "default-src 'none'",
    `script-src 'unsafe-inline' ${origin}`.trim(),
    "style-src 'unsafe-inline'",
    "img-src data: blob:",
    "font-src data:",
    "connect-src 'none'",
  ].join("; ");

  // フレーム内スクリプト。ここは Console のモジュールではなく素の DOM しか使えない。
  // i18n-exempt-start: 中身はスクリプトとその日本語コメントで、画面に出る文字列は無い。
  const boot = `
(function () {
  // 外部を指す既定値を全部潰す（docs/65 §65.2.1-3）。P1 で STENCIL_PATH だけ
  // 自オリジンの CP プロキシへ向ける。
  //
  // **空文字では潰せない**: ビューアは window.X = window.X || "https://…" の形で
  // 既定値を入れるので、"" は falsy ＝ 外部の既定値が生き残る。実測では
  // DRAW_MATH_URL を "" にしたまま viewer.diagrams.net/math4/es5/startup.js を
  // 取りに行き、CSP が止めていた。ネットワークに出ない dead value を入れる。
  var DEAD = "about:blank";
  window.PROXY_URL = DEAD;
  window.STYLE_PATH = DEAD;
  window.SHAPES_PATH = DEAD;
  window.STENCIL_PATH = DEAD;
  window.DRAW_MATH_URL = DEAD;
  window.GRAPH_IMAGE_PATH = DEAD;
  window.CSS_PATH = DEAD;
  window.DRAWIO_BASE_URL = DEAD;
  window.DRAWIO_SERVER_URL = DEAD;
  window.DRAWIO_LIGHTBOX_URL = DEAD;
  window.DRAWIO_LOG_URL = DEAD;

  var MSG = ${JSON.stringify(DRAWIO_MSG)};
  var viewer = null;
  var dark = ${dark ? "true" : "false"};

  function post(m) {
    m.af = MSG;
    parent.postMessage(m, "*");
  }

  // ビューアの失敗は非同期に起きることがあり、握り潰すとペインが理由なく空になる。
  // 何が起きても親に 1 行返す（親はこれを「図として開けない」と表示に変える）。
  //
  // **window.onerror では受け取れない**: 読み込まれるビューア本体が自分のロガーで
  // window.onerror を上書きするため、代入した関数は静かに外される（実測）。
  // 上書きできない addEventListener 側で受ける。
  window.addEventListener("error", function (e) {
    post({ t: "error", code: "parse", detail: String((e && e.message) || e).slice(0, 200) });
  });

  // ビューアはコンテナの寸法を自分で決めにかかる（graph.resizeContainer / 
  // updateContainerHeight）。ペインいっぱいに保ちたいので、**インライン指定の
  // 幅・高さ**を与えたうえで resize を切る —— addSizeHandler の分岐がインライン
  // style.height の有無を見ているため、CSS クラスだけでは効かない（実測: 
  // 860x520 のフレームでコンテナが 181x341 まで縮み、図が左上に貼り付いた）。
  function host() {
    var old = document.getElementById("c");
    var el = document.createElement("div");
    el.id = "c";
    sizeToViewport(el);
    old.parentNode.replaceChild(el, old);
    return el;
  }

  function sizeToViewport(el) {
    el.style.width = document.documentElement.clientWidth + "px";
    el.style.height = document.documentElement.clientHeight + "px";
  }

  // 収め直しはビューア自身の fitGraph に任せる。allowZoomIn が既定 false なので
  // maxFitScale = 1 ＝ **大きい図は縮小され、小さい図は原寸のまま**中央に出る。
  // 自前で graph.fit を呼ぶとビューアの initialViewState と食い違い、ズームボタンの
  // 基準がずれる。
  function fit(v) {
    if (!v || typeof v.fitGraph !== "function") return;
    sizeToViewport(v.graph.container);
    v.fitGraph();
  }

  function render(xml, isDark) {
    dark = !!isDark;
    // srcdoc は一度しか組み立てない（作り直すと 4MB を読み直す）。テーマ切り替えは
    // 描画要求として届くので、背景もここで追従させる。
    document.documentElement.style.background = dark ? "#1e1e1e" : "#ffffff";
    var el = host();
    if (!xml || !xml.trim()) {
      post({ t: "error", code: "empty" });
      return;
    }
    el.setAttribute(
      "data-mxgraph",
      JSON.stringify({
        xml: xml,
        // lightbox は出さない（外部持ち出しの経路）。ページ送り・ズーム・レイヤーだけ。
        toolbar: "pages zoom layers",
        "toolbar-nohide": true,
        lightbox: 0,
        nav: true,
        resize: 0,
        center: true,
        "dark-mode": dark,
        highlight: "#3572b0",
      })
    );
    try {
      GraphViewer.createViewerForElement(el, function (v) {
        viewer = v;
        // 収まってから状態を返す（scale はヘッダの倍率表示とハーネスの判定に使う）。
        requestAnimationFrame(function () {
          fit(v);
          postState(v);
        });
        // ページ送り・レイヤー操作の結果も同じ形で返す（ヘッダの「n / m」のため）。
        if (v.addListener) {
          v.addListener("graphChanged", function () {
            postState(v);
          });
        }
      });
    } catch (e) {
      post({ t: "error", code: "parse" });
    }
  }

  function postState(v) {
    post({
      t: "rendered",
      pages: v.diagrams ? v.diagrams.length : 1,
      page: (v.currentPage || 0) + 1,
      scale: Math.round(v.graph.view.scale * 100) / 100,
    });
  }

  // 親の描画要求はビューア本体（4MB）の読み込みより先に着き得る。取りこぼすと
  // ペインが永久に空になるので、着いた要求は必ず保持して load 後に流す。
  var pending = null;
  var loaded = false;

  window.addEventListener("message", function (e) {
    var m = e.data;
    if (!m || m.af !== MSG || m.t !== "render") return;
    if (!loaded) {
      pending = m;
      return;
    }
    render(m.xml, m.dark);
  });

  window.addEventListener("load", function () {
    loaded = true;
    // ステンシルの遅延取得は P0 では行わない。connect-src 'none' で塞がってはいるが、
    // 試行そのものを止めておく方が失敗の見え方が素直になる（docs/65 §65.5）。
    if (window.mxStencilRegistry) mxStencilRegistry.dynamicLoading = false;
    post({ t: "ready" });
    if (pending) {
      var m = pending;
      pending = null;
      render(m.xml, m.dark);
    }
  });

  // ペインの寸法が変わったら描き直さずに収め直す。
  if (window.ResizeObserver) {
    new ResizeObserver(function () {
      if (viewer) fit(viewer);
    }).observe(document.body);
  }
})();`;
  // i18n-exempt-end

  // i18n-exempt-start: HTML の骨組み（表示文言は含まない）。
  return `<!doctype html>
<html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${attrEscape(csp)}">
<style>
html,body{margin:0;padding:0;height:100%;overflow:hidden;background:${dark ? "#1e1e1e" : "#ffffff"};}
#c{position:absolute;inset:0;}
</style></head>
<body><div id="c"></div>
<script>${boot}</script>
<script src="${attrEscape(viewerUrl)}"></script>
</body></html>`;
  // i18n-exempt-end
}
