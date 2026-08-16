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
  rel: (p: string) => p,
}));

const { FileView } = await import("./FileView.tsx");

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

  it("ただの .xml は今までどおりソースのまま", async () => {
    served = { content: "<project><modelVersion>4.0.0</modelVersion></project>", editable: true, truncated: false };
    await render({ filePath: "repos/x/pom.xml" });
    expect(frame()).toBeNull();
    expect(groupButtons("Diagram display mode")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).not.toBeNull();
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
