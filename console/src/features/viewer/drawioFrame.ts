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
// **フレームは何ひとつ自分で取りに行かない** —— 図の XML も、ビューア本体 4MB の
// ソースも、親が取得して postMessage で渡す（CSP は default-src 'none' のまま、
// script-src に外部オリジンすら要らない）。
//
// ビューアを `<script src>` で読ませてはならない（実測・2026-08-16 の不具合）:
// オリジンを持たないフレームからの要求は **cross-site 扱いになり、SameSite=Lax の
// セッション cookie が付かない**。CP の authGate は `/assets/*` を素通ししないので
// 401 になり、GraphViewer が未定義のまま render に入って「図として解釈できない」と
// 誤報告していた。取得は資格情報を持つ親の仕事、フレームは受け取るだけ。

/** 親 → フレーム / フレーム → 親 のメッセージ。`af` で他の postMessage と混ざらないようにする。 */
export const DRAWIO_MSG = "af-drawio" as const;

/** 親 → フレーム。`boot` は 1 回だけ（4MB のソースを載せる）、`render` は何度でも。 */
export type DrawioFrameRequest =
  | { af: typeof DRAWIO_MSG; t: "boot"; src: string }
  | { af: typeof DRAWIO_MSG; t: "render"; xml: string; dark: boolean };

export type DrawioFrameEvent =
  | { af: typeof DRAWIO_MSG; t: "ready" }
  | { af: typeof DRAWIO_MSG; t: "booted" }
  | { af: typeof DRAWIO_MSG; t: "rendered"; pages: number; page: number; scale: number }
  /** `boot` はビューアを評価できなかった、`parse` は図として読めなかった。
   *  **この 2 つを混ぜてはいけない** —— 読み込み失敗を「図が壊れている」と表示すると、
   *  原因がファイル側にあるように見えて調査が丸ごと逸れる（実際に起きた）。 */
  | { af: typeof DRAWIO_MSG; t: "error"; code: "boot" | "parse" | "empty" };

const FRAME_EVENTS = ["ready", "booted", "rendered", "error"];

/** フレームから来たイベントか判定する（postMessage は誰でも送れる）。 */
export function isDrawioFrameEvent(data: unknown): data is DrawioFrameEvent {
  if (!data || typeof data !== "object") return false;
  const m = data as { af?: unknown; t?: unknown };
  return m.af === DRAWIO_MSG && typeof m.t === "string" && FRAME_EVENTS.includes(m.t);
}

// srcdoc は属性値なので、埋め込む文字列はダブルクォートまで含めて必ずエスケープする。
function attrEscape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export interface DrawioFrameOptions {
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
export function drawioFrameSrcdoc({ dark }: DrawioFrameOptions): string {
  // ビューアのソースは親が postMessage で渡し、インライン script として評価する。
  // したがって **外部オリジンを script-src に載せる必要が無い**（載せると、フレームが
  // 自分で取りに行く経路が復活してしまう）。
  const csp = [
    "default-src 'none'",
    "script-src 'unsafe-inline'",
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
  var booted = false;
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
    post({
      t: "error",
      code: booted ? "parse" : "boot",
      detail: String((e && e.message) || e).slice(0, 200),
    });
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
    if (!booted || typeof GraphViewer === "undefined") {
      // ここに来るのは「ビューアを評価できていない」ということでしかない。
      // 図のせいにしない（誤ったメッセージは調査をファイル側へ逸らす）。
      post({ t: "error", code: "boot" });
      return;
    }
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

  // ビューア本体（4MB）のソースを親から受け取り、インライン script として評価する。
  // 同期実行なので、append から戻った時点で GraphViewer が居る（居なければ boot 失敗）。
  function boot(src) {
    if (booted) return;
    var el = document.createElement("script");
    el.textContent = src;
    document.body.appendChild(el);
    if (typeof GraphViewer === "undefined") {
      post({ t: "error", code: "boot" });
      return;
    }
    booted = true;
    // ステンシルの遅延取得は P0 では行わない。connect-src 'none' で塞がってはいるが、
    // 試行そのものを止めておく方が失敗の見え方が素直になる（docs/65 §65.5）。
    if (window.mxStencilRegistry) mxStencilRegistry.dynamicLoading = false;
    post({ t: "booted" });
    if (pending) {
      var m = pending;
      pending = null;
      render(m.xml, m.dark);
    }
  }

  // 描画要求は boot より先に着き得るので、その 1 通だけは保持して boot 後に流す。
  var pending = null;

  window.addEventListener("message", function (e) {
    var m = e.data;
    if (!m || m.af !== MSG) return;
    if (m.t === "boot") {
      boot(m.src);
      return;
    }
    if (m.t !== "render") return;
    if (!booted) {
      pending = m;
      return;
    }
    render(m.xml, m.dark);
  });

  // 「この文書はもう受け取れる」を親へ知らせる。**親はこれを待ってから送る**——
  // iframe を作った直後に送ると、まだ srcdoc の文書が無く、メッセージは初期の
  // about:blank に配達されて消える（実測: 0/10/50ms は消え、200ms で通った）。
  post({ t: "ready" });

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
</body></html>`;
  // i18n-exempt-end
}
