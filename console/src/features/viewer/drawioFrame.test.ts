// フレーム HTML の組み立て（docs/log/65 §65.3）。ここで守るのは「外部へ出る経路と
// 隔離が、文字列として本当に入っているか」だけ。描画そのものは実ブラウザでしか
// 確かめられないので scripts/drawio/check.mjs が見る。
import { describe, expect, it } from "vitest";
import { drawioFrameSrcdoc, isDrawioFrameEvent, DRAWIO_MSG } from "./drawioFrame.ts";

const html = (dark = false) => drawioFrameSrcdoc({ dark });

describe("drawioFrameSrcdoc", () => {
  it("外部の既定 URL を空文字ではなく dead value で潰す", () => {
    const s = html();
    // "" は falsy で `window.X = window.X || "https://…"` に負ける（実測で
    // DRAW_MATH_URL が viewer.diagrams.net を取りに行った）。
    expect(s).not.toMatch(/window\.DRAW_MATH_URL = "";/);
    for (const name of [
      "PROXY_URL",
      "STYLE_PATH",
      "SHAPES_PATH",
      "STENCIL_PATH",
      "DRAW_MATH_URL",
      "GRAPH_IMAGE_PATH",
      "CSS_PATH",
      "DRAWIO_BASE_URL",
      "DRAWIO_SERVER_URL",
      "DRAWIO_LIGHTBOX_URL",
      "DRAWIO_LOG_URL",
    ]) {
      expect(s).toContain(`window.${name} = DEAD;`);
    }
    expect(s).toContain('var DEAD = "about:blank";');
  });

  it("フレームは何ひとつ自分で取りに行かない", () => {
    const s = html();
    expect(s).toContain("default-src 'none'");
    expect(s).toContain("connect-src 'none'");
    // **`<script src>` を復活させてはならない**: オリジンを持たないフレームからの要求は
    // cross-site 扱いで SameSite=Lax のセッション cookie が付かず、CP の authGate に
    // 401 で弾かれる（2026-08-16 の不具合）。ビューアの本文は親が postMessage で渡す。
    expect(s).not.toMatch(/<script[^>]+src=/);
    // 外部オリジンを script-src に載せる必要も無い（載せれば取りに行く経路が復活する）。
    expect(s).toContain("script-src 'unsafe-inline';");
  });

  it("ステンシルもフレームには取りに行かせない（docs/log/65 §65.5.4）", () => {
    const s = html();
    // ビューア自身の遅延取得を切ったままにする。true に戻すと、認証を通れない要求・
    // CSP の穴・失敗後に再試行しない詰まり方が **まとめて** 戻ってくる（どれも実測）。
    expect(s).toContain("mxStencilRegistry.dynamicLoading = false");
    // したがって STENCIL_PATH / SHAPES_PATH は dead value のまま。ここを CP へ向ける
    // 変更は「フレームが自分で取りに行く」への逆戻りを意味する。
    expect(s).toMatch(/window\.STENCIL_PATH = DEAD;/);
    expect(s).toMatch(/window\.SHAPES_PATH = DEAD;/);
    // 取得が親の仕事である以上、connect-src を開ける理由が無い（上のケースと二重）。
    expect(s).toContain("connect-src 'none'");
  });

  it("必要なセットは basename ではなく libraries の読み替えで決める", () => {
    const s = html();
    // `basename + ".xml"` 決め打ちは rackGeneral → rack/general.xml のような
    // 読み替えを落として 404 になる。実機で効くのは libraries を引く経路だけ。
    expect(s).toContain("mxStencilRegistry.libraries");
    // 割り出しは **展開済みのモデル**から。生 XML の走査は圧縮された <diagram> で
    // 何も見つけられない（実測 rawSeen=0）。
    expect(s).toContain("v.graph.model");
    // 差し込みは render のやり直しではなく refresh（見ていた倍率と位置を壊さない）。
    expect(s).toContain("parseStencilSets");
    expect(s).toContain("viewer.graph.refresh()");
  });

  it("lightbox をツールバーにも設定にも出さない", () => {
    const s = html();
    // 図面を app.diagrams.net へ持ち出す唯一のボタン。ツールバー文字列に現れたら赤。
    expect(s).toContain('toolbar: "pages zoom layers"');
    expect(s).toContain("lightbox: 0");
  });

  it("テーマで初期背景が変わる", () => {
    expect(html(false)).toContain("background:#ffffff");
    expect(html(true)).toContain("background:#1e1e1e");
  });

  it("dark-mode は真偽値ではなく文字列で渡す", () => {
    // isDarkMode() は "dark" / "auto" と比較するので、true は黙って「ライト」になる。
    // 暗い背景に黒い既定文字が載って読めなくなる（docs/log/65 §65.11-10）。
    // 値はフレームの中で描画時に決まるので、ここで見えるのは式そのもの。
    // **実際に暗色描画になったか**は scripts/drawio/check.mjs が isDarkMode() で検算する。
    expect(html()).toContain('"dark-mode": dark ? "dark" : "light"');
    expect(html()).not.toMatch(/"dark-mode": dark,/);
  });

  it("ジェスチャを自分で配線する（GraphViewer は何もしない）", () => {
    const s = html();
    // ホイールは passive 既定では preventDefault が効かないので明示する。
    expect(s).toContain('"wheel"');
    expect(s).toContain("{ passive: false }");
    expect(s).toContain('"pointerdown"');
    expect(s).toContain('"dblclick"');
    // スマホのピンチを図に届かせるには touch-action を切るしかない。
    expect(s).toContain("touch-action:none");
    // 収め直しは fitGraph ではなく initialViewState（幅が同じだと fitGraph は無反応）。
    expect(s).toContain("initialViewState");
  });

  it("window.onerror ではなく addEventListener で失敗を拾う", () => {
    // ビューア本体が window.onerror を上書きするため（実測）。
    const s = html();
    expect(s).toContain('window.addEventListener("error"');
    expect(s).not.toContain("window.onerror =");
  });
});

describe("isDrawioFrameEvent", () => {
  it("自分のフレームの形だけを通す", () => {
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "ready" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "booted" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "rendered", pages: 1, page: 1, scale: 1 })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "error", code: "parse" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "render", xml: "" })).toBe(false);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "boot", src: "" })).toBe(false);
    expect(isDrawioFrameEvent({ t: "ready" })).toBe(false);
    expect(isDrawioFrameEvent("ready")).toBe(false);
    expect(isDrawioFrameEvent(null)).toBe(false);
  });
});
