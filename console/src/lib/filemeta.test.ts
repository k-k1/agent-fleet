import { describe, expect, it } from "vitest";
import { documentFormat, documentLabel, isDrawioFile, isPdfFile, langFor, looksLikeDrawioXml, resolveMarkdownFileTarget } from "./filemeta.ts";

describe("resolveMarkdownFileTarget", () => {
  it("resolves an agent absolute source citation under home", () => {
    expect(resolveMarkdownFileTarget("/home/dev/repos/app/src/main.ts:635", "", "/home/dev/repos/other")).toEqual({
      path: "repos/app/src/main.ts",
      line: 635,
    });
  });

  it("resolves line and column suffixes", () => {
    expect(resolveMarkdownFileTarget("~/repos/app/main.ts:12:7")).toEqual({
      path: "repos/app/main.ts",
      line: 12,
      column: 7,
    });
  });

  it("resolves GitHub line fragments", () => {
    expect(resolveMarkdownFileTarget("repos/app/main.ts#L9C3", "", "repos/other")).toEqual({
      path: "repos/app/main.ts",
      line: 9,
      column: 3,
    });
  });

  it("resolves relative links from a turn cwd", () => {
    expect(resolveMarkdownFileTarget("src/main.ts:4", "", "/home/dev/repos/app")).toEqual({
      path: "repos/app/src/main.ts",
      line: 4,
    });
  });

  it("keeps document-root links within the repository", () => {
    expect(resolveMarkdownFileTarget("/docs/guide.md", "repos/app/README.md")).toEqual({
      path: "repos/app/docs/guide.md",
    });
  });

  it("keeps an allowed absolute document root when following a relative link", () => {
    expect(resolveMarkdownFileTarget(
      "06-agents.md",
      "/usr/local/share/agent-fleet/docs/member/README.md",
    )).toEqual({
      path: "/usr/local/share/agent-fleet/docs/member/06-agents.md",
    });
  });

  it("decodes Japanese paths and ignores external URLs", () => {
    expect(resolveMarkdownFileTarget("docs/%E8%A8%AD%E8%A8%88.md:20", "", "repos/app")).toEqual({
      path: "repos/app/docs/設計.md",
      line: 20,
    });
    expect(resolveMarkdownFileTarget("https://example.com:443/a.ts:8")).toBeNull();
  });
});

// ── drawio の判定（docs/log/65 §65.4）─────────────────────────────────────────
describe("isDrawioFile", () => {
  it("拡張子で決まるのは .drawio / .dio だけ", () => {
    expect(isDrawioFile("repos/a/design.drawio")).toBe(true);
    expect(isDrawioFile("repos/a/design.DIO")).toBe(true);
    expect(isDrawioFile("repos/a/design.drawio.svg")).toBe(false); // 画像として既に開ける
    expect(isDrawioFile("repos/a/notes.md")).toBe(false);
  });

  it(".xml は中身の頭を見る（見せなければ図とは言わない）", () => {
    const head = '<?xml version="1.0" encoding="UTF-8"?>\n<mxfile host="app.diagrams.net">';
    expect(isDrawioFile("repos/a/diagram.xml", head)).toBe(true);
    expect(isDrawioFile("repos/a/diagram.xml")).toBe(false);
    expect(isDrawioFile("repos/a/pom.xml", "<project><modelVersion/></project>")).toBe(false);
  });

  it("2 MiB 超で本文が取れなくても .drawio は図として開ける", () => {
    // Agent は maxEditorFileBytes を超えると content を差し替える。図そのものは
    // download 経由で取り直すので、拡張子の判断だけで面を出してよい。
    expect(isDrawioFile("repos/a/big.drawio", "(file too large to preview)")).toBe(true);
  });
});

describe("looksLikeDrawioXml", () => {
  it("XML 宣言・BOM・コメントを読み飛ばす", () => {
    expect(looksLikeDrawioXml("<mxfile>")).toBe(true);
    expect(looksLikeDrawioXml("<mxGraphModel dx=\"1\">")).toBe(true);
    expect(looksLikeDrawioXml('\uFEFF<?xml version="1.0"?><!-- made by drawio --><mxfile a="1">')).toBe(true);
    expect(looksLikeDrawioXml("<mxfileish>")).toBe(false);
    expect(looksLikeDrawioXml("<svg xmlns=\"http://www.w3.org/2000/svg\">")).toBe(false);
    expect(looksLikeDrawioXml("")).toBe(false);
  });
});

describe("langFor", () => {
  it("図のソース面は XML として色を付ける", () => {
    expect(langFor("a/b.drawio")).toBe("xml");
    expect(langFor("a/b.dio")).toBe("xml");
  });
});

describe("isPdfFile", () => {
  it("matches by extension, case-insensitively", () => {
    expect(isPdfFile("repos/a/spec.pdf")).toBe(true);
    expect(isPdfFile("repos/a/SPEC.PDF")).toBe(true);
    expect(isPdfFile("repos/a/spec.pdf.bak")).toBe(false);
    expect(isPdfFile("repos/a/pdf")).toBe(false);
  });
});

describe("documentFormat", () => {
  it("maps Office extensions onto anydoc's format names", () => {
    expect(documentFormat("repos/a/plan.docx")).toBe("docx");
    expect(documentFormat("repos/a/plan.DOCM")).toBe("docx"); // マクロ付きも同じ解析器
    expect(documentFormat("repos/a/book.xlsm")).toBe("xlsx");
    expect(documentFormat("repos/a/deck.ppsx")).toBe("pptx"); // スライドショー形式
    expect(documentFormat("repos/a/note.odt")).toBe("odt");
  });

  it("leaves text formats alone", () => {
    // csv も anydoc は読めるが、すでにテキストとして開けている。変換に回すと
    // コードビューと編集面を失う（docs/log/82 §82.4）。
    expect(documentFormat("repos/a/data.csv")).toBe("");
    expect(documentFormat("repos/a/README.md")).toBe("");
    expect(documentFormat("repos/a/spec.pdf")).toBe(""); // PDF は描く面が別にある
    expect(documentFormat("repos/a/docx")).toBe("");
  });

  it("labels by the extension as written", () => {
    expect(documentLabel("repos/a/plan.docx")).toBe("DOCX");
    expect(documentLabel("repos/a/deck.PPTX")).toBe("PPTX");
  });
});
