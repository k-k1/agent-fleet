// DocPreview — lightweight preview of Word / Excel / PowerPoint (docs/log/82 §82.4).
//
// anydoc converts to GFM, which is rendered by the Console's MarkdownView. It does not reproduce
// the appearance: page layout, shape positions and cell colours are lost, and embedded images
// become alt text. Hence the "simple preview" note at the top of the pane and the download link
// to the original kept next to it — looking faithful would be the more dangerous option, because
// a table stripped of its formatting would then be taken at face value.
//
// Conversion itself is under 1ms in WASM (measured, docs/log/82 §82.2). The only slow part is
// fetching the 2.9MB WASM the first time, and only someone who opens this format pays it.
import { useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { anydocFailure, toMarkdown, type AnydocFailure, type AnydocFormat } from "./anydoc.ts";
import type { ScrollMemoryRef } from "./parts/useScrollMemory.ts";

/** Files larger than this are not converted. The WASM loads the whole document into memory, so
 *  pointing at the download is more honest than killing the tab on a huge attachment. */
export const MAX_DOC_BYTES = 40 * 1024 * 1024;

interface DocPreviewProps {
  /** URL of the raw bytes (the download endpoint). */
  src: string;
  /** Format guessed from the extension, passed as a hint for when the content is inconclusive. */
  format?: string;
  /** File size (from api/fs/file), used only for the size limit. */
  size?: number;
  /** Base path used to resolve links inside the Markdown. */
  basePath?: string;
  onOpenFile?: (path: string, line?: number, column?: number, openInNew?: boolean) => void;
  onOpenDir?: (path: string) => void;
  /** Scroll-position memory (parts/useScrollMemory). The element that scrolls is .md-scroll. */
  scrollMemory?: ScrollMemoryRef;
}

type State =
  | { phase: "loading" }
  | { phase: "ready"; markdown: string }
  | { phase: "failed"; reason: AnydocFailure | "too_large" };

export function DocPreview({ src, format, size, basePath, onOpenFile, onOpenDir, scrollMemory }: DocPreviewProps) {
  const tr = useT();
  const [state, setState] = useState<State>({ phase: "loading" });
  const boxRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let alive = true;
    setState({ phase: "loading" });
    if (size != null && size > MAX_DOC_BYTES) {
      setState({ phase: "failed", reason: "too_large" });
      return;
    }
    const run = async () => {
      const res = await fetch(src);
      if (!res.ok) throw Object.assign(new Error(`http ${res.status}`), { code: "failed" });
      const bytes = new Uint8Array(await res.arrayBuffer());
      if (!alive) return;
      const markdown = await toMarkdown(bytes, (format || undefined) as AnydocFormat | undefined);
      if (!alive) return;
      setState({ phase: "ready", markdown });
    };
    run().catch((e) => {
      if (alive) setState({ phase: "failed", reason: anydocFailure(e) });
    });
    return () => {
      alive = false;
    };
  }, [src, format, size]);

  if (state.phase === "loading") {
    return (
      <div className="docpreview">
        <p className="docpreview-status muted">{tr("view.doc.loading")}</p>
      </div>
    );
  }

  if (state.phase === "failed") {
    // Never fail silently to a blank pane: always say why it cannot be read and that the original
    // can still be opened.
    const message =
      state.reason === "too_large"
        ? tr("view.doc.too_large")
        : state.reason === "encrypted"
          ? tr("view.doc.encrypted")
          : state.reason === "needsOcr"
            ? tr("view.doc.needs_ocr")
            : state.reason === "unsupported"
              ? tr("view.doc.unsupported")
              : tr("view.doc.cannot_convert");
    return (
      <div className="docpreview">
        <p className="docpreview-status muted">{message}</p>
        <p className="docpreview-status muted">{tr("view.doc.download_hint")}</p>
      </div>
    );
  }

  return (
    <div className="docpreview" ref={boxRef}>
      <p className="docpreview-note">
        <Icon name="info" /> {tr("view.doc.simple_preview_note")}
      </p>
      <div className="md-scroll" ref={scrollMemory}>
        <MarkdownView source={state.markdown} basePath={basePath} onOpenFile={onOpenFile} onOpenDir={onOpenDir} />
      </div>
    </div>
  );
}
