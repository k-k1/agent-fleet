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
/** 見ている場所。テーマ切り替えでフレームを作り直すとき、そのまま返して復元する。 */
export interface DrawioViewState {
  /** 表示中のページ（diagram の id）。番号ではなく id —— 番号はページの増減でずれる。 */
  pageId: string | null;
  scale: number;
  tx: number;
  ty: number;
  /** 利用者が自分でズーム / パンしたか。していないなら復元せず収め直す。 */
  adjusted: boolean;
}

export type DrawioFrameRequest =
  | { af: typeof DRAWIO_MSG; t: "boot"; src: string }
  | { af: typeof DRAWIO_MSG; t: "render"; xml: string; dark: boolean; restore?: DrawioViewState | null };

export type DrawioFrameEvent =
  | { af: typeof DRAWIO_MSG; t: "ready" }
  | { af: typeof DRAWIO_MSG; t: "booted" }
  | {
      af: typeof DRAWIO_MSG;
      t: "rendered";
      pages: number;
      page: number;
      scale: number;
      /** ビューアが実際に暗色描画になっているか（要求どおりかを親／ハーネスが検算する）。
       *  **画素を数える判定は当てにならない** —— 暗色にならないまま暗い背景に載った絵は
       *  図形の明るい塗りのせいで「明るい画素」がむしろ増える（実測 40778 対 2387）。 */
      darkMode: boolean;
      /** 作り直したフレームへ引き継ぐための現在地。 */
      pageId: string | null;
      tx: number;
      ty: number;
      adjusted: boolean;
    }
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
  // 収まりの基準倍率と、利用者が自分で動かしたかどうか。**動かした後にペインの寸法が
  // 変わっても勝手に収め直さない** —— 拡大して見ている最中に元へ戻されるのが一番困る。
  var fitScale = 1;
  var adjusted = false;

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

  // ツールバーはコンテナの外（上）に置かれ、コンテナには marginTop が付く。
  // その分を引かないと下端がフレームからはみ出す。
  function sizeToViewport(el) {
    var chrome = parseInt(el.style.marginTop || "0", 10) || 0;
    el.style.width = document.documentElement.clientWidth + "px";
    el.style.height = Math.max(0, document.documentElement.clientHeight - chrome) + "px";
  }

  // 収め直しはビューア自身の fitGraph に任せる。allowZoomIn が既定 false なので
  // maxFitScale = 1 ＝ **大きい図は縮小され、小さい図は原寸のまま**中央に出る。
  // 自前で graph.fit を呼ぶとビューアの initialViewState と食い違い、ズームボタンの
  // 基準がずれる。
  function fit(v) {
    if (!v || typeof v.fitGraph !== "function") return;
    sizeToViewport(v.graph.container);
    v.fitGraph();
    fitScale = v.graph.view.scale;
    adjusted = false;
  }

  function render(xml, isDark, restore) {
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
        // **真偽値では効かない**: isDarkMode() は "dark" / "auto" という文字列と
        // 比較するので、true を渡すと黙って「ライト」になる。既定の文字色は黒のまま
        // 暗い背景に載り、既定色のラベルが黒地に黒で消える（実測: 文字 0 / 背景 30 の
        // 輝度＝コントラスト比 1.3:1）。docs/65 §65.11-10。
        "dark-mode": dark ? "dark" : "light",
        highlight: "#3572b0",
        // 復元するページ。**番号ではなく id** で指す（graphConfig.pageId）——
        // 番号はページの増減でずれるうえ、ここで欲しいのは「さっき見ていたページ」。
        pageId: restore && restore.pageId ? restore.pageId : undefined,
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
          // 先に収める: fitScale と initialViewState（ダブルタップの戻り先）を確定させる。
          fit(v);
          // そのうえで、利用者が自分で動かしていた場所へ戻す。動かしていなければ
          // 収まりのままにする —— 何もしていない人にとっては、それが正しい状態。
          if (restore && restore.adjusted) {
            v.graph.view.scaleAndTranslate(restore.scale, restore.tx, restore.ty);
            adjusted = true;
          }
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

  // ── 操作（ズーム / パン）─────────────────────────────────────────────
  // GraphViewer は**何も配線していない**（init は pinchEnabled=false・setPanning(false)、
  // ホイールの購読も無し）。ツールバーのボタンしか無いので、ここで足す:
  //   Ctrl/⌘＋ホイール … 指した点を軸に拡大縮小（トラックパッドのピンチも同じ経路で来る）
  //   素のホイール      … 上下左右へパン
  //   2 本指ピンチ      … 中点を軸に拡大縮小
  //   1 本指ドラッグ    … パン
  //   ダブルクリック/タップ … 収まり ↔ 等倍（等倍で収まっているときは 2 倍）
  var MIN_SCALE = 0.05;
  var MAX_SCALE = 16;

  function graph() {
    return viewer && viewer.graph;
  }

  // ツールバー上の操作は素通しする（ボタンを潰さないため）。
  function onToolbar(target) {
    return !!(viewer && viewer.toolbar && target && viewer.toolbar.contains(target));
  }

  // コンテナ左上を原点とする座標。ズームの軸に使う。
  function localPoint(clientX, clientY) {
    var g = graph();
    if (!g) return { x: 0, y: 0 };
    var box = g.container.getBoundingClientRect();
    return { x: clientX - box.left, y: clientY - box.top };
  }

  // 画面上の 1 点を固定したまま倍率を変える。mxGraph は screen = (graph + translate) * scale
  // なので、その点が動かない translate は translate + p * (1/s' - 1/s) で出る。
  function zoomAt(nextScale, px, py) {
    var g = graph();
    if (!g) return;
    var s = g.view.scale;
    var s2 = Math.max(MIN_SCALE, Math.min(MAX_SCALE, nextScale));
    if (!isFinite(s2) || s2 === s) return;
    var t = g.view.translate;
    adjusted = true;
    g.view.scaleAndTranslate(s2, t.x + px * (1 / s2 - 1 / s), t.y + py * (1 / s2 - 1 / s));
    postState(viewer);
  }

  function panBy(dx, dy) {
    var g = graph();
    if (!g || (!dx && !dy)) return;
    var s = g.view.scale;
    var t = g.view.translate;
    adjusted = true;
    g.view.scaleAndTranslate(s, t.x + dx / s, t.y + dy / s);
  }

  // 最初に見せた状態へ戻す。**fitGraph は使えない** —— あれはコンテナ幅が前回と同じなら
  // 何もしない実装（N == t で早期 return）なので、寸法が変わらないダブルタップでは
  // 無反応になる（実測: 4.37 倍のまま動かなかった）。ビューアが控えている
  // initialViewState をそのまま戻す方が速く、ズームボタンの基準とも食い違わない。
  function resetView(v) {
    var g = v && v.graph;
    if (!g) return;
    var st = g.initialViewState;
    if (st && st.translate) {
      g.view.scaleAndTranslate(st.scale, st.translate.x, st.translate.y);
      fitScale = st.scale;
    } else {
      fit(v);
    }
    adjusted = false;
    postState(v);
  }

  // ダブルクリック / ダブルタップ。収まっているなら等倍へ寄る、それ以外は収め直す。
  // 収まりが既に等倍のときだけ 2 倍にする（「押しても何も起きない」を作らない）。
  function toggleZoom(px, py) {
    var g = graph();
    if (!g) return;
    var atFit = Math.abs(g.view.scale - fitScale) < 0.01;
    if (!atFit) {
      resetView(viewer);
      return;
    }
    zoomAt(fitScale >= 0.99 ? 2 : 1, px, py);
  }

  function installGestures() {
    var el = document.documentElement;

    // ホイールは passive で登録されると preventDefault が効かない。明示的に外す。
    el.addEventListener(
      "wheel",
      function (e) {
        if (!graph() || onToolbar(e.target)) return;
        // deltaMode: 0=px, 1=行, 2=ページ。行/ページのブラウザでも同じ効き目にする。
        var unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
        e.preventDefault();
        var p = localPoint(e.clientX, e.clientY);
        // ctrlKey はトラックパッドのピンチでも立つ（ブラウザ共通の約束）。
        if (e.ctrlKey || e.metaKey) {
          zoomAt(graph().view.scale * Math.exp((-e.deltaY * unit) / 400), p.x, p.y);
        } else {
          panBy(-e.deltaX * unit, -e.deltaY * unit);
        }
      },
      { passive: false }
    );

    var points = {};   // pointerId -> {x,y}
    var pinch = null;  // {dist, scale}
    var lastTap = 0;
    var lastTapPos = null;

    el.addEventListener("pointerdown", function (e) {
      if (!graph() || onToolbar(e.target)) return;
      points[e.pointerId] = { x: e.clientX, y: e.clientY };
      if (el.setPointerCapture) {
        try {
          el.setPointerCapture(e.pointerId);
        } catch (err) {
          /* 別の要素が既に捕捉している等 —— パンできればよいので黙って続ける */
        }
      }
      var ids = Object.keys(points);
      if (ids.length === 2) {
        var a = points[ids[0]];
        var b = points[ids[1]];
        pinch = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale: graph().view.scale };
      }
    });

    el.addEventListener("pointermove", function (e) {
      var prev = points[e.pointerId];
      if (!prev || !graph()) return;
      var cur = { x: e.clientX, y: e.clientY };
      points[e.pointerId] = cur;
      var ids = Object.keys(points);
      if (ids.length >= 2 && pinch) {
        var a = points[ids[0]];
        var b = points[ids[1]];
        var dist = Math.hypot(a.x - b.x, a.y - b.y);
        if (pinch.dist > 0) {
          var mid = localPoint((a.x + b.x) / 2, (a.y + b.y) / 2);
          zoomAt((pinch.scale * dist) / pinch.dist, mid.x, mid.y);
        }
        return;
      }
      panBy(cur.x - prev.x, cur.y - prev.y);
    });

    function endPointer(e) {
      if (!points[e.pointerId]) return;
      delete points[e.pointerId];
      if (Object.keys(points).length < 2) pinch = null;
      if (!graph()) return;
      // ダブルタップ判定。dblclick はタッチでは環境差が大きいので自分で見る。
      var now = e.timeStamp || 0;
      var pos = { x: e.clientX, y: e.clientY };
      if (lastTapPos && now - lastTap < 350 && Math.hypot(pos.x - lastTapPos.x, pos.y - lastTapPos.y) < 30) {
        var p = localPoint(pos.x, pos.y);
        lastTap = 0;
        lastTapPos = null;
        toggleZoom(p.x, p.y);
        return;
      }
      lastTap = now;
      lastTapPos = pos;
    }
    el.addEventListener("pointerup", endPointer);
    el.addEventListener("pointercancel", endPointer);

    el.addEventListener("dblclick", function (e) {
      if (!graph() || onToolbar(e.target)) return;
      e.preventDefault();
      var p = localPoint(e.clientX, e.clientY);
      toggleZoom(p.x, p.y);
    });
  }

  function postState(v) {
    var page = v.diagrams && v.diagrams[v.currentPage || 0];
    post({
      t: "rendered",
      pages: v.diagrams ? v.diagrams.length : 1,
      page: (v.currentPage || 0) + 1,
      scale: Math.round(v.graph.view.scale * 100) / 100,
      darkMode: !!(v.isDarkMode && v.isDarkMode()),
      pageId: page && page.getAttribute ? page.getAttribute("id") : null,
      tx: v.graph.view.translate.x,
      ty: v.graph.view.translate.y,
      adjusted: adjusted,
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
    installGestures();
    // ステンシルの遅延取得は P0 では行わない。connect-src 'none' で塞がってはいるが、
    // 試行そのものを止めておく方が失敗の見え方が素直になる（docs/65 §65.5）。
    if (window.mxStencilRegistry) mxStencilRegistry.dynamicLoading = false;
    post({ t: "booted" });
    if (pending) {
      var m = pending;
      pending = null;
      render(m.xml, m.dark, m.restore);
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
    render(m.xml, m.dark, m.restore);
  });

  // 「この文書はもう受け取れる」を親へ知らせる。**親はこれを待ってから送る**——
  // iframe を作った直後に送ると、まだ srcdoc の文書が無く、メッセージは初期の
  // about:blank に配達されて消える（実測: 0/10/50ms は消え、200ms で通った）。
  post({ t: "ready" });

  // ペインの寸法が変わったら描き直さずに収め直す。
  if (window.ResizeObserver) {
    new ResizeObserver(function () {
      if (!viewer) return;
      if (adjusted) {
        // 倍率と位置はそのまま。コンテナの大きさだけ追従させる。
        sizeToViewport(viewer.graph.container);
        return;
      }
      fit(viewer);
    }).observe(document.body);
  }
})();`;
  // i18n-exempt-end

  // i18n-exempt-start: HTML の骨組み（表示文言は含まない）。
  return `<!doctype html>
<html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${attrEscape(csp)}">
<style>
/* touch-action:none が無いと、スマホのピンチはページ拡大に取られて図に届かない。
   overscroll-behavior は、端まで動かしたときに親ペインが引っ張られるのを止める。 */
html,body{margin:0;padding:0;height:100%;overflow:hidden;background:${dark ? "#1e1e1e" : "#ffffff"};
  touch-action:none;overscroll-behavior:contain;-webkit-user-select:none;user-select:none;}
#c{position:absolute;inset:0;}
</style></head>
<body><div id="c"></div>
<script>${boot}</script>
</body></html>`;
  // i18n-exempt-end
}
