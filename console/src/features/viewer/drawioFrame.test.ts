// フレーム HTML の組み立て（docs/65 §65.3）。ここで守るのは「外部へ出る経路と
// 隔離が、文字列として本当に入っているか」だけ。描画そのものは実ブラウザでしか
// 確かめられないので scripts/drawio/check.mjs が見る。
import { describe, expect, it } from "vitest";
import { drawioFrameSrcdoc, isDrawioFrameEvent, scriptOriginOf, DRAWIO_MSG } from "./drawioFrame.ts";

const html = (dark = false) =>
  drawioFrameSrcdoc({ viewerUrl: "https://console.example/assets/viewer-abc123.js", dark });

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

  it("ビューアの実体だけを script-src に許し、通信は塞ぐ", () => {
    const s = html();
    expect(s).toContain("default-src 'none'");
    expect(s).toContain("script-src 'unsafe-inline' https://console.example");
    expect(s).toContain("connect-src 'none'");
    expect(s).toContain('<script src="https://console.example/assets/viewer-abc123.js"></script>');
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

  it("window.onerror ではなく addEventListener で失敗を拾う", () => {
    // ビューア本体が window.onerror を上書きするため（実測）。
    const s = html();
    expect(s).toContain('window.addEventListener("error"');
    expect(s).not.toContain("window.onerror =");
  });
});

describe("scriptOriginOf", () => {
  it("URL のオリジンを返し、壊れた URL では空を返す", () => {
    expect(scriptOriginOf("https://a.example/x/y.js")).toBe("https://a.example");
    expect(scriptOriginOf("not a url")).toBe("");
  });
});

describe("isDrawioFrameEvent", () => {
  it("自分のフレームの形だけを通す", () => {
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "ready" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "rendered", pages: 1, page: 1, scale: 1 })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "error", code: "parse" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "render", xml: "" })).toBe(false);
    expect(isDrawioFrameEvent({ t: "ready" })).toBe(false);
    expect(isDrawioFrameEvent("ready")).toBe(false);
    expect(isDrawioFrameEvent(null)).toBe(false);
  });
});
