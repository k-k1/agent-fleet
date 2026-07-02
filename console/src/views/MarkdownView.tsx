import { useEffect, useRef } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { dirName, joinPath, isExternalUrl, slug } from "../lib/filemeta.js";

// MarkdownView renders Markdown to sanitized HTML, highlights fenced code blocks,
// turns ```mermaid blocks into rendered diagrams (lazy-loaded), and wires links:
// external URLs open in a new tab, in-page #anchors scroll, and repo-relative
// links open that file in the viewer via onOpenFile.
let mermaidSeq = 0;

interface MarkdownViewProps {
  source?: string;
  basePath?: string;
  onOpenFile?: (path: string) => void;
}

export default function MarkdownView({ source, basePath = "", onOpenFile }: MarkdownViewProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let alive = true;

    const rawHtml = marked.parse(source ?? "", { gfm: true, breaks: false }) as string;
    el.innerHTML = DOMPurify.sanitize(rawHtml);

    // Give headings slug ids so in-page #anchors can resolve.
    el.querySelectorAll("h1,h2,h3,h4,h5,h6").forEach((h) => {
      if (!h.id) h.id = slug(h.textContent || "");
    });

    wireLinks(el, basePath, onOpenFile);

    // Syntax-highlight every fenced code block except mermaid (handled below).
    el.querySelectorAll<HTMLElement>("pre > code").forEach((code) => {
      if (code.classList.contains("language-mermaid")) return;
      try {
        hljs.highlightElement(code);
      } catch {}
    });

    // Render mermaid diagrams, replacing their <pre> with the produced SVG.
    const blocks = [...el.querySelectorAll<HTMLElement>("pre > code.language-mermaid")];
    if (blocks.length) {
      import("mermaid").then(({ default: mermaid }) => {
        if (!alive) return;
        mermaid.initialize({ startOnLoad: false, theme: "dark", securityLevel: "strict" });
        blocks.forEach(async (code) => {
          const src = code.textContent || "";
          const id = "mmd-" + ++mermaidSeq;
          try {
            const { svg } = await mermaid.render(id, src);
            if (!alive || !code.parentElement) return;
            const wrap = document.createElement("div");
            wrap.className = "mermaid-diagram";
            wrap.innerHTML = svg;
            code.parentElement.replaceWith(wrap);
          } catch {
            // leave the source block in place on a render error
          }
        });
      });
    }

    return () => {
      alive = false;
    };
  }, [source, basePath, onOpenFile]);

  return <div className="markdown" ref={ref} />;
}

// wireLinks classifies and rewires every <a> in the rendered markdown.
function wireLinks(el: HTMLElement, basePath: string, onOpenFile?: (path: string) => void) {
  // Repo-absolute (/foo) links resolve from the repo root; everything else from
  // the markdown file's own directory.
  const m = basePath.match(/^(repos\/[^/]+)\//);
  const repoRoot = m ? m[1] : dirName(basePath);
  const fileDir = dirName(basePath);

  el.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((a) => {
    const href = a.getAttribute("href") || "";

    if (href.startsWith("#")) {
      a.classList.add("anchor-link");
      a.addEventListener("click", (e) => {
        e.preventDefault();
        const id = decodeURIComponent(href.slice(1));
        let t: Element | null = null;
        try {
          t = el.querySelector("#" + CSS.escape(id));
        } catch {}
        t?.scrollIntoView({ behavior: "smooth", block: "start" });
      });
      return;
    }

    if (isExternalUrl(href)) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.classList.add("ext-link");
      return;
    }

    // Repo-internal relative link → open that file in the viewer.
    a.classList.add("repo-link");
    a.addEventListener("click", (e) => {
      e.preventDefault();
      if (!onOpenFile) return;
      const p = href.split("#")[0].split("?")[0];
      if (!p) return;
      const base = p.startsWith("/") ? repoRoot : fileDir;
      const target = joinPath(base, p.replace(/^\/+/, ""));
      if (target) onOpenFile(target);
    });
  });
}
