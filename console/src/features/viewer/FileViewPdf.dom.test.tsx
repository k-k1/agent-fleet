// File ペインが PDF をどう扱うか（docs/82）。
//
// ここで見るのは「面の選択と受け渡し」だけ —— どのファイルで PDF の面に降り、生バイトの
// URL が渡り、情報バーが PDF として読めるか。実際に絵が出るかは canvas の話なので
// jsdom では確かめられない（`npm --prefix console run pdf:check` が実ブラウザで見る）。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let served: Record<string, unknown> = {};

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    return served;
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));

// PdfView 自体は canvas と pdf.js を持つので、ここでは「呼ばれた・何を渡された」だけ見る。
const pdfProps: { src: string; onMeta?: (m: { pages: number }) => void }[] = [];
vi.mock("./PdfView.tsx", () => ({
  PdfView: (props: { src: string; onMeta?: (m: { pages: number }) => void }) => {
    pdfProps.push(props);
    return <div data-surface="pdf" />;
  },
}));

const { FileView } = await import("./FileView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const binaryFile = (path: string, size = 12345) => ({
  path,
  size,
  binary: true,
  truncated: false,
  editable: false,
  editabilityReason: "binary",
});

async function render(filePath: string): Promise<void> {
  await act(async () => {
    root!.render(<FileView filePath={filePath} paneId="pane-1" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const surface = () => host.querySelector("[data-surface]")?.getAttribute("data-surface") ?? null;
const tags = () => [...host.querySelectorAll(".fi-tag")].map((e) => e.textContent);
const meta = () => host.querySelector(".fi-meta")?.textContent ?? "";

beforeEach(() => {
  pdfProps.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("the File pane on a PDF", () => {
  it("shows the PDF surface instead of the binary placeholder", async () => {
    // 退行の型: api/fs/file は PDF を binary:true としか答えないので、PDF の分岐を
    // バイナリの分岐より後ろに置くと、いつまでも「(バイナリ, 12.1 KB)」のままになる。
    served = binaryFile("repos/x/report.pdf");
    await render("repos/x/report.pdf");
    expect(surface()).toBe("pdf");
    expect(host.textContent).not.toContain("binary");
  });

  it("hands the viewer the raw-bytes URL", async () => {
    served = binaryFile("repos/x/report.pdf");
    await render("repos/x/report.pdf");
    expect(pdfProps.at(-1)?.src).toBe("/dl/repos/x/report.pdf");
  });

  it("labels the file as a PDF and reports the page count once it is known", async () => {
    served = binaryFile("repos/x/report.pdf");
    await render("repos/x/report.pdf");
    expect(tags()).toContain("PDF");
    expect(meta()).not.toContain("pages");

    await act(async () => {
      pdfProps.at(-1)?.onMeta?.({ pages: 12 });
    });
    expect(meta()).toContain("12 pages");
    // 行数はテキストの話。PDF に出しては嘘になる。
    expect(meta()).not.toContain("lines");
  });

  it("offers no editing surface", async () => {
    served = binaryFile("repos/x/report.pdf");
    await render("repos/x/report.pdf");
    expect(host.querySelector(".file-save-btn")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).toBeNull();
  });

  it("leaves other binaries on the placeholder", async () => {
    served = binaryFile("repos/x/blob.bin");
    await render("repos/x/blob.bin");
    expect(surface()).toBeNull();
    expect(host.textContent).toContain("binary");
  });

  it("drops the previous document's page count when the pane is retargeted", async () => {
    served = binaryFile("repos/x/a.pdf");
    await render("repos/x/a.pdf");
    await act(async () => {
      pdfProps.at(-1)?.onMeta?.({ pages: 12 });
    });
    expect(meta()).toContain("12 pages");

    served = binaryFile("repos/x/b.pdf");
    await render("repos/x/b.pdf");
    expect(meta()).not.toContain("12 pages");
  });
});

describe("the bundled pdf.js assets", () => {
  it("addresses cMaps by version, under the app's base URI", async () => {
    // cMap が引けないと、フォントを埋め込んでいない日本語 PDF が空白になる。
    // 版をパスに入れるのは、assets/ が immutable で配られるため（vite.config.js）。
    const { pdfjsAssetURL } = await import("./pdfjs.ts");
    const url = pdfjsAssetURL("cmaps");
    if (!url) return; // define の無い文脈（同梱アセットを使わない）
    expect(url.startsWith(document.baseURI)).toBe(true);
    expect(url).toMatch(/\/assets\/pdfjs\/\d+\.\d+\.\d+\/cmaps\/$/);
  });
});
