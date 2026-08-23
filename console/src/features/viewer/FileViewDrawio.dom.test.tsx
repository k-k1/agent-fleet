// File ペインが `.drawio` をどう出すか（docs/65 §65.4）。
//
// 描画そのものは jsdom では確かめられない（iframe の中のスクリプトは走らない）。
// ここで守るのは「どの面が出るか」と「iframe の権限」——後者は実ブラウザでも
// 見えにくいのに、緩めた瞬間に隔離が全部無くなる種類の設定。
// 図が実際に描けることは scripts/drawio/check.mjs が実ブラウザで見る。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { revisionOf } from "../editor/buffer.ts";
import { clearDirtyRegistryForTests } from "../editor/dirtyRegistry.ts";

const DIAGRAM = '<mxfile host="app.diagrams.net"><diagram id="a" name="ページ1"></diagram></mxfile>';
const VIEWER_SRC = "/* drawio viewer source */";

let served = { content: DIAGRAM, editable: true, truncated: false };

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const { content, editable, truncated } = served;
    return {
      path: "repos/x/design.drawio",
      size: content.length,
      binary: false,
      truncated,
      editable,
      editabilityReason: editable ? null : "read_only_root",
      content,
      ...(editable ? { revision: revisionOf(content) } : {}),
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  // **恒等関数にしない。** rel() を通したかどうかを判定できなくなる（素の相対パスでも
  // 同じ文字列になり、パスを剥がすプロキシ下で壊れる実装が緑のまま通る）。
  rel: (p: string) => `/agent-fleet/${p}`,
}));

const { FileView } = await import("./FileView.tsx");
const { DrawioView } = await import("./DrawioView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
let fetched: string[] = [];

async function render(props: { filePath?: string; targetLine?: number; openMode?: "view" | "edit" } = {}) {
  await act(async () => {
    root!.render(<FileView filePath={props.filePath ?? "repos/x/design.drawio"} paneId="pane-1" {...props} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const frame = () => host.querySelector("iframe.drawio-frame") as HTMLIFrameElement | null;
// 図の面が「見えているか」。畳んだときは外さず hidden にするだけなので、
// 要素の有無ではなく hidden で見る（作り直すと 4MB を読み直すため）。
const diagramVisible = () => {
  const shell = host.querySelector(".file-diagram-shell");
  return !!shell && !shell.hasAttribute("hidden");
};
const groupButtons = (label: string) => {
  const group = host.querySelector(`[aria-label="${label}"]`);
  return group ? [...group.querySelectorAll("button")].map((b) => b.textContent) : null;
};
const clickMode = (label: string) => {
  const button = [...host.querySelectorAll('[aria-label="Diagram display mode"] button')].find(
    (b) => b.textContent === label,
  ) as HTMLButtonElement;
  act(() => button.click());
};
const editorVisible = () => {
  const shell = host.querySelector(".file-editor-shell");
  return !!shell && !shell.hasAttribute("hidden");
};

beforeEach(() => {
  clearDirtyRegistryForTests();
  served = { content: DIAGRAM, editable: true, truncated: false };
  fetched = [];
  vi.stubGlobal("fetch", (url: string) => {
    fetched.push(String(url));
    const viewer = String(url).includes("viewer-static");
    return Promise.resolve({ ok: true, text: async () => (viewer ? VIEWER_SRC : DIAGRAM) } as Response);
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.unstubAllGlobals();
  clearDirtyRegistryForTests();
});

describe(".drawio の面", () => {
  it("図として開き、Markdown の 3 モード群も view/edit タブも出さない", async () => {
    await render();
    expect(frame()).not.toBeNull();
    expect(groupButtons("Diagram display mode")).toEqual(["Diagram", "Edit"]);
    expect(groupButtons("Markdown display mode")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).toBeNull();
  });

  it("iframe には allow-scripts しか与えない", async () => {
    await render();
    // allow-same-origin を足すと Console と同じ権限になり、allow-popups を足すと
    // lightbox の window.open（図面を app.diagrams.net へ渡す）が通る。
    expect(frame()!.getAttribute("sandbox")).toBe("allow-scripts");
  });

  it("図は fs/file ではなく download から取る（2 MiB の打ち切りを避ける）", async () => {
    await render();
    expect(fetched).toEqual(["/dl/repos/x/design.drawio"]);
  });

  it("本文が打ち切られていても図の面は出る", async () => {
    served = { content: "(file too large to preview)", editable: false, truncated: true };
    await render();
    expect(frame()).not.toBeNull();
    // 編集面が無いので、もう一方は読み取り専用の「ソース」と名乗る。
    expect(groupButtons("Diagram display mode")).toEqual(["Diagram", "Source"]);
  });

  it("ソース面へ切り替えると図を畳んで編集面を出す（フレームは残す）", async () => {
    await render();
    expect(diagramVisible()).toBe(true);
    expect(editorVisible()).toBe(false);
    clickMode("Edit");
    expect(diagramVisible()).toBe(false);
    expect(editorVisible()).toBe(true);
    // 畳んでも作り直さない: 4MB の読み直しとズーム位置の消失を避ける。
    expect(frame()).not.toBeNull();
  });

  it("行を指して開いた引用はソース面に着地し、図はまだ作らない", async () => {
    await render({ targetLine: 3 });
    // 一度も図を見ていないうちは取得もしない。
    expect(frame()).toBeNull();
    expect(fetched).toEqual([]);
    expect(editorVisible()).toBe(true);
  });

  it("mxfile を収めた .xml も図として開く", async () => {
    await render({ filePath: "repos/x/diagram.xml" });
    expect(frame()).not.toBeNull();
  });

  it("図には朗読を出さない（読み上げる本文が無い）", async () => {
    await render();
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(labels.some((l) => l?.includes("Read aloud"))).toBe(false);
  });

  it("ただの .xml は今までどおりソースのまま", async () => {
    served = { content: "<project><modelVersion>4.0.0</modelVersion></project>", editable: true, truncated: false };
    await render({ filePath: "repos/x/pom.xml" });
    expect(frame()).toBeNull();
    expect(groupButtons("Diagram display mode")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).not.toBeNull();
    // 図でないテキストからは朗読を取り上げない（外す条件が広すぎないことの確認）。
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(labels.some((l) => l?.includes("Read aloud"))).toBe(true);
  });
});

// ── フレームとの手順（docs/65 §65.11-7・§65.11-8）───────────────────────────
// jsdom は srcdoc の中のスクリプトを走らせないので、フレームの発言はこちらで作る。
// ここで守るのは「親が何を、いつ送るか」——実機だけで壊れた 2 件の再発防止。
describe("フレームとの手順", () => {
  const fromFrame = (data: Record<string, unknown>) => {
    const win = frame()!.contentWindow!;
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", ...data }, source: win }));
    });
  };
  const posts = () =>
    (frame()!.contentWindow!.postMessage as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => c[0] as Record<string, unknown>,
    );
  const spyOnFrame = () => {
    vi.spyOn(frame()!.contentWindow!, "postMessage").mockImplementation(() => {});
  };

  it("ready を待ってからビューア本体を渡し、booted を待って描画を頼む", async () => {
    await render();
    spyOnFrame();
    // 作った直後には何も送らない: srcdoc の文書ができる前に送るとメッセージは
    // 初期の about:blank に配達されて消える（実測）。
    expect(posts()).toEqual([]);

    fromFrame({ t: "ready" });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // ビューアは **親が** 取る（フレームからでは Lax cookie が付かず 401 になる）。
    expect(fetched.some((u) => u.includes("viewer-static"))).toBe(true);
    expect(posts().map((m) => m.t)).toEqual(["boot"]);
    expect(posts()[0].src).toBe(VIEWER_SRC);

    fromFrame({ t: "booted" });
    expect(posts().map((m) => m.t)).toEqual(["boot", "render"]);
    expect(posts()[1].xml).toBe(DIAGRAM);
  });

  it("ステンシルはフレームの申告を受けて親が CP から取り、中身を返す", async () => {
    await render();
    spyOnFrame();
    fetched = [];
    // フレームは「これが要る」としか言わない。**取りに行くのは親**（フレームからでは
    // オリジンが無く Lax cookie が付かないので authGate に 401 で弾かれる）。
    fromFrame({ t: "stencils", sets: ["aws4.xml", "rack/general.xml"] });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // **rel() を通すこと。** 素の相対パスは文書 URL に対して解決されるので、
    // パスを剥がすプロキシの下や `/open/...` の深い URL で行き先がずれる。
    expect(fetched).toEqual([
      "/agent-fleet/api/drawio/stencils/aws4.xml",
      "/agent-fleet/api/drawio/stencils/rack/general.xml",
    ]);
    const back = posts().filter((m) => m.t === "stencils");
    expect(back).toHaveLength(1);
    expect((back[0].xml as string[]).length).toBe(2);
  });

  it("ステンシルが取れなくても図はそのまま（エラーにしない）", async () => {
    await render();
    spyOnFrame();
    vi.stubGlobal("fetch", () => Promise.resolve({ ok: false, status: 502, text: async () => "" } as Response));
    fromFrame({ t: "stencils", sets: ["aws4.xml"] });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // 閉域では図案だけが空になり、枠と色は残る。**図は正しく開けているのだから、
    // 利用者に見せる異常ではない** —— 画面には何も出さない。
    expect(host.textContent).not.toContain("stencil");
    expect(frame()!.hidden).toBe(false);
    // ただしフレームには「取れなかった」と伝える。**伝えないと、upstream の 1 回の
    // 瞬断でそのペインの寿命いっぱいアイコンが欠ける**（頼んだ済みのまま固定される）。
    const back = posts().filter((m) => m.t === "stencils");
    expect(back).toHaveLength(1);
    expect(back[0].xml).toEqual([]);
    expect(back[0].missing).toEqual(["aws4.xml"]);
  });

  it("ビューアを読み込めなかったときに「図が壊れている」と言わない", async () => {
    await render();
    spyOnFrame();
    fromFrame({ t: "error", code: "boot" });
    const note = host.querySelector(".drawio-note")!.textContent ?? "";
    expect(note).toContain("Could not load the diagram viewer");
    expect(note).not.toContain("not readable as drawio");
  });

  it("図として読めないときはそのまま伝える", async () => {
    await render();
    spyOnFrame();
    fromFrame({ t: "error", code: "parse" });
    expect(host.querySelector(".drawio-note")!.textContent).toContain("not readable as drawio");
  });
});

// ── テーマ切り替え（docs/65 §65.11-12）─────────────────────────────────────
// drawio は 1 つの文書内でのテーマ往復を想定していない（実測: 同じフレームに描き直しを
// 頼むと見出しが消え、ラベルはライト時の白いピル＋黒文字のまま残る）。**フレームごと
// 作り直し、見ていた場所を引き継ぐ**のが契約で、ここではその配線を見る。
describe("テーマ切り替え", () => {
  const mount = async (dark: boolean) => {
    await act(async () => {
      root!.render(<DrawioView filePath="repos/x/design.drawio" dark={dark} />);
    });
    await act(async () => {
      await Promise.resolve();
    });
  };
  const frameEl = () => host.querySelector("iframe.drawio-frame") as HTMLIFrameElement;
  const postsOf = (el: HTMLIFrameElement) =>
    (el.contentWindow!.postMessage as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => c[0] as Record<string, unknown>,
    );
  const drive = async (el: HTMLIFrameElement) => {
    vi.spyOn(el.contentWindow!, "postMessage").mockImplementation(() => {});
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", t: "ready" }, source: el.contentWindow }));
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", t: "booted" }, source: el.contentWindow }));
    });
  };

  it("テーマが変わったらフレームを作り直し、ページと倍率を引き継ぐ", async () => {
    await mount(false);
    const first = frameEl();
    await drive(first);
    expect(postsOf(first).map((m) => m.t)).toEqual(["boot", "render"]);
    // 最初の描画には引き継ぐものが無い。
    expect(postsOf(first)[1].restore).toBeNull();

    // 利用者が拡大して 2 ページ目を見ている状態をフレームから伝える。
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { af: "af-drawio", t: "rendered", pages: 2, page: 2, scale: 2.5, darkMode: false, pageId: "p2", tx: 12, ty: 34, adjusted: true },
          source: first.contentWindow,
        }),
      );
    });

    // ダークへ切り替える。
    await mount(true);
    const second = frameEl();
    // **同じ要素を使い回さない**（作り直しが目的）。
    expect(second).not.toBe(first);
    await drive(second);
    const render = postsOf(second).find((m) => m.t === "render")!;
    expect(render.dark).toBe(true);
    // 見ていた場所がそのまま渡る。ページは番号ではなく id で指す。
    expect(render.restore).toEqual({ pageId: "p2", scale: 2.5, tx: 12, ty: 34, adjusted: true });
  });

  it("自分で動かしていなければ復元させない（収まりのままにする）", async () => {
    await mount(false);
    const first = frameEl();
    await drive(first);
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { af: "af-drawio", t: "rendered", pages: 1, page: 1, scale: 1, darkMode: false, pageId: "p1", tx: 5, ty: 6, adjusted: false },
          source: first.contentWindow,
        }),
      );
    });
    await mount(true);
    const second = frameEl();
    await drive(second);
    const render = postsOf(second).find((m) => m.t === "render")!;
    // 渡しはするが adjusted=false なので、フレーム側は収め直しを選ぶ。
    expect((render.restore as { adjusted: boolean }).adjusted).toBe(false);
  });
});
