// DocPreview — Word / Excel / PowerPoint の簡易プレビュー（docs/log/82 §82.4）。
//
// anydoc で GFM に変換し、Console の MarkdownView にそのまま載せる。**見た目は再現しない**:
// ページ体裁も図形の位置もセルの色も落ち、埋め込み画像は alt text になる。だから面の頭に
// 「簡易プレビュー」と出し、原本を開くための導線（情報バーのダウンロード）を隣に残す
// —— 再現しているように見せる方が、書式の消えた表を鵜呑みにされるぶん危ない。
//
// 変換は WASM で 1ms 未満（実測・docs/log/82 §82.2）。時間がかかるのは 2.9MB の WASM を
// 初回に取ってくるところだけで、それは「この形式を開いた人」だけが払う。
import { useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { anydocFailure, toMarkdown, type AnydocFailure, type AnydocFormat } from "./anydoc.ts";
import type { ScrollMemoryRef } from "./parts/useScrollMemory.ts";

/** これより大きいファイルは変換に回さない。WASM は全体をメモリに載せるので、
 *  巨大な添付でタブごと落とすより、ダウンロードへ誘導する方が正直。 */
export const MAX_DOC_BYTES = 40 * 1024 * 1024;

interface DocPreviewProps {
  /** 生バイトの URL（download エンドポイント）。 */
  src: string;
  /** 拡張子から分かる形式。中身から判定できなかったときの手掛かりとして渡す。 */
  format?: string;
  /** ファイルの大きさ（api/fs/file の size）。上限判定にだけ使う。 */
  size?: number;
  /** Markdown 内のリンクを解決する基準パス。 */
  basePath?: string;
  onOpenFile?: (path: string, line?: number, column?: number, openInNew?: boolean) => void;
  onOpenDir?: (path: string) => void;
  /** 表示位置の記憶（parts/useScrollMemory）。スクロールするのは .md-scroll。 */
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
    // 白い面のまま黙らない。「なぜ読めないか」と「原本を開けばよい」を必ず出す。
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
