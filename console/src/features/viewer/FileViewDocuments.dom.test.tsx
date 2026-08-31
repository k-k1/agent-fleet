// File ペインが PDF と Office 文書をどう扱うか（docs/82）。
//
// ここで見るのは「面の選択と受け渡し」だけ —— どのファイルでどの面に降り、生バイトの
// URL と形式が渡り、情報バーが何と読めるか。実際に絵が出るか・変換が通るかは canvas と
// WASM の話で jsdom では確かめられない（`npm --prefix console run pdf:check` と
// `doc:check` が実ブラウザで見る）。
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

// PdfView / DocPreview 自体は canvas と WASM を持つので、ここでは「呼ばれた・何を
// 渡された」だけ見る（実物は scripts/pdf・scripts/doc のハーネスが実ブラウザで見る）。
const pdfProps: { src: string; onMeta?: (m: { pages: number }) => void }[] = [];
vi.mock("./PdfView.tsx", () => ({
  PdfView: (props: { src: string; onMeta?: (m: { pages: number }) => void }) => {
    pdfProps.push(props);
    return <div data-surface="pdf" />;
  },
}));

const docProps: { src: string; format?: string; size?: number }[] = [];
vi.mock("./DocPreview.tsx", () => ({
  DocPreview: (props: { src: string; format?: string; size?: number }) => {
    docProps.push(props);
    return <div data-surface="doc" />;
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
  docProps.length = 0;
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

describe("the File pane on an Office document", () => {
  it("shows the simple preview instead of the binary placeholder", async () => {
    served = binaryFile("repos/x/plan.docx");
    await render("repos/x/plan.docx");
    expect(surface()).toBe("doc");
    expect(host.textContent).not.toContain("binary");
  });

  it("hands the converter the raw bytes, the format and the size", async () => {
    // size は上限判定に要る（WASM は全体をメモリに載せる）。落とすと巨大な添付で
    // タブごと落ちるまで気づけない。
    served = binaryFile("repos/x/book.xlsx", 999);
    await render("repos/x/book.xlsx");
    expect(docProps.at(-1)).toMatchObject({ src: "/dl/repos/x/book.xlsx", format: "xlsx", size: 999 });
  });

  it("labels the file by its extension", async () => {
    served = binaryFile("repos/x/deck.pptx");
    await render("repos/x/deck.pptx");
    expect(tags()).toContain("PPTX");
    expect(meta()).not.toContain("lines");
  });

  it("leaves csv and other text files alone", async () => {
    // csv は anydoc も読めるが、すでにテキストとして読めている。変換に回すと
    // コードビューも編集面も失う。
    served = {
      path: "repos/x/data.csv",
      size: 12,
      binary: false,
      truncated: false,
      editable: false,
      editabilityReason: "read_only_root",
      content: "a,b\n1,2\n",
    };
    await render("repos/x/data.csv");
    expect(surface()).toBeNull();
    expect(docProps).toHaveLength(0);
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
