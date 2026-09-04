// 読む側の面（.file-viewer-shell）。ファイルの種別と選ばれたモードから
// 「どの面を描くか」を 1 本の分岐で決める —— 画像 / PDF / Office 文書 /
// バイナリ / 大きすぎるテキスト / スライド / Markdown プレビュー / コード。
//
// 順序に意味がある分岐なので、条件は元のまま 1 つの三項演算子の連なりで持つ。
// フックは持たず、状態はすべて FileView 側にある。
import { downloadURL } from "../../../core/api/client.ts";
import { baseName, countLines, humanSize } from "../../../lib/filemeta.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { CodeView, type LineMarks } from "../CodeView.tsx";
import { DocPreview } from "../DocPreview.tsx";
import { ImageView } from "../ImageView.tsx";
import { MarkdownView } from "../MarkdownView.tsx";
import { MarpView } from "../MarpView.tsx";
import { PdfView } from "../PdfView.tsx";
import type { FileSurfaces } from "../fileMode.ts";
import type { ScrollMemoryFactory } from "./useScrollMemory.ts";

export interface FileViewerShellProps {
  hidden: boolean;
  filePath: string;
  err: string;
  /** null = まだ読めていない（…を出す）。 */
  loaded: boolean;
  size?: number;
  binary?: boolean;
  isImage: boolean;
  isText: boolean;
  imgMode: "preview" | "source";
  onImgDims(dims: { w: number; h: number }): void;
  isPdf: boolean;
  onPdfMeta(meta: { pages: number }): void;
  isDoc: boolean;
  docFmt?: string;
  onOpenFile(path: string, line?: number, column?: number, openInNew?: boolean): void;
  onOpenDir(path: string): void;
  huge: boolean;
  viewContent: string;
  preview: FileSurfaces["preview"];
  previewSource: string;
  html: string;
  lineNumbers: boolean;
  wrap: boolean;
  minimap: boolean;
  marks: LineMarks | null;
  targetLine?: number;
  targetColumn?: number;
  /** 面ごとの「表示位置の記憶」ref を配る（FileView が持つ）。面が変われば別の
   *  記憶になるので、分岐ごとに違う名前を引く。 */
  scrollMemory: ScrollMemoryFactory;
}

export function FileViewerShell(props: FileViewerShellProps) {
  const tr = useT();
  const { filePath, isImage, isText, imgMode, isPdf, isDoc } = props;
  return (
    <div className="file-viewer-shell" hidden={props.hidden}>
      {props.err ? (
        <pre className="filebody muted">({props.err})</pre>
      ) : !props.loaded ? (
        <pre className="filebody muted">…</pre>
      ) : isImage && (!isText || imgMode === "preview") ? (
        <ImageView src={downloadURL(filePath)} alt={baseName(filePath)} onLoad={props.onImgDims} />
      ) : isPdf ? (
        // PDF は「バイナリ」の一歩手前で拾う（docs/log/82）。api/fs/file は中身を返さず
        // binary:true だけを返すので、ここから先は download の生バイトが情報源。
        <PdfView src={downloadURL(filePath)} onMeta={props.onPdfMeta} scrollMemory={props.scrollMemory("pdf")} />
      ) : isDoc ? (
        <DocPreview
          src={downloadURL(filePath)}
          format={props.docFmt}
          size={props.size}
          basePath={filePath}
          onOpenFile={props.onOpenFile}
          onOpenDir={props.onOpenDir}
          scrollMemory={props.scrollMemory("doc")}
        />
      ) : props.binary ? (
        <pre className="filebody muted">({tr("view.binary")}, {humanSize(props.size)})</pre>
      ) : props.huge ? (
        <pre className="filebody fb-plain" ref={props.scrollMemory("plain")}>{props.viewContent}</pre>
      ) : props.preview === "slides" ? (
        <MarpView source={props.previewSource} />
      ) : props.preview === "normal" ? (
        <div className="md-scroll" ref={props.scrollMemory("preview")}>
          <MarkdownView source={props.previewSource} basePath={filePath} onOpenFile={props.onOpenFile} onOpenDir={props.onOpenDir} />
        </div>
      ) : (
        <CodeView
          html={props.html}
          lines={countLines(props.viewContent)}
          lineNumbers={props.lineNumbers}
          wrap={props.wrap}
          minimap={props.minimap}
          marks={props.marks}
          targetLine={props.targetLine}
          targetColumn={props.targetColumn}
          scrollMemory={props.scrollMemory("code")}
        />
      )}
    </div>
  );
}
