// How the File pane handles PDFs and Office documents (docs/log/82).
//
// Only surface selection and hand-off are covered here: which file lands on which surface, that
// the raw-bytes URL and the format are passed on, and what the info bar reads. Whether anything
// is actually painted or converted is canvas and WASM, which jsdom cannot check
// (`npm --prefix console run pdf:check` and `doc:check` do that in a real browser).
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

// PdfView / DocPreview themselves need canvas and WASM, so only "was it called and with what"
// is checked here; the real thing is covered by the scripts/pdf and scripts/doc harnesses.
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
    // Regression shape: api/fs/file reports a PDF only as binary:true, so putting the PDF
    // branch after the binary branch leaves the pane stuck on "(binary, 12.1 KB)" forever.
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
    // A line count is a text notion; showing one for a PDF would be a lie.
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
    // size is needed for the limit check, because the WASM converter loads the whole file into
    // memory. Drop it and a huge attachment is only noticed when it takes the tab down.
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
    // anydoc can read csv too, but it already reads as text; sending it through the converter
    // would lose both the code view and the editing surface.
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
    // Without reachable cMaps, a Japanese PDF that embeds no fonts renders blank. The version
    // is in the path because assets/ is served as immutable (vite.config.js).
    const { pdfjsAssetURL } = await import("./pdfjs.ts");
    const url = pdfjsAssetURL("cmaps");
    if (!url) return; // no define in this context, i.e. the bundled assets are not used
    expect(url.startsWith(document.baseURI)).toBe(true);
    expect(url).toMatch(/\/assets\/pdfjs\/\d+\.\d+\.\d+\/cmaps\/$/);
  });
});
