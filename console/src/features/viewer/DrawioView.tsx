// DrawioView — `.drawio` を図として表示する面（docs/65・ADR 0046）。
//
// 描画そのものは同梱の drawio ビューアが iframe の中で行う（drawioFrame.ts）。この
// コンポーネントの仕事は 3 つだけ:
//   1. 図の XML を **`api/fs/download` から** 取る。`api/fs/file` は 2 MiB で打ち切る
//      ので（maxEditorFileBytes）、画像を埋めた図がそこで「(file too large…)」に
//      化ける。download 側にサイズ上限は無い。
//   2. iframe に postMessage で流し込む（フレームは資格情報も外向き通信も持たない）。
//   3. フレームが返す状態（ページ数・倍率）を親へ渡す。
import { useEffect, useMemo, useRef, useState } from "react";
import viewerAssetUrl from "../../../vendor/drawio/viewer-static.min.js?url";
import { downloadURL } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { DRAWIO_MSG, drawioFrameSrcdoc, isDrawioFrameEvent } from "./drawioFrame.ts";

export interface DrawioState {
  pages: number;
  page: number;
  scale: number;
}

interface DrawioViewProps {
  filePath: string;
  dark: boolean;
  /** ヘッダに「n / m」「%」を出すための状態。読めなかったときは null。 */
  onState?: (state: DrawioState | null) => void;
  /** 図として読めなかったときに「ソースを見る」導線を出すためのフック。 */
  onShowSource?: () => void;
}

export function DrawioView({ filePath, dark, onState, onShowSource }: DrawioViewProps) {
  const tr = useT();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [xml, setXml] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [frameErr, setFrameErr] = useState("");
  const readyRef = useRef(false);
  const onStateRef = useRef(onState);
  onStateRef.current = onState;

  // ビューア本体（4MB）の URL は絶対 URL にしてからフレームへ渡す。srcdoc の中の
  // 相対 URL は親の base に対して解決されるため、パスを剥がすプロキシの下や
  // /open/... のような深い URL では解決先がずれる。
  const viewerUrl = useMemo(() => new URL(viewerAssetUrl, document.baseURI).href, []);
  // srcdoc は **一度だけ** 組み立てる。作り直すと iframe が再読み込みになり 4MB を
  // 読み直すので、テーマ切り替えや別ファイルは postMessage 側で扱う。
  const srcdoc = useMemo(
    () => drawioFrameSrcdoc({ viewerUrl, dark }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [viewerUrl],
  );

  useEffect(() => {
    let alive = true;
    setXml(null);
    setErr("");
    setFrameErr("");
    onStateRef.current?.(null);
    fetch(downloadURL(filePath))
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((text) => alive && setXml(text))
      .catch(() => alive && setErr(tr("view.cannot_load")));
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath]);

  // フレームからのイベント。sandbox のオリジンは opaque（"null"）なので origin では
  // 絞れない。**発信元の window で照合する**。
  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.source !== frameRef.current?.contentWindow) return;
      if (!isDrawioFrameEvent(event.data)) return;
      const msg = event.data;
      if (msg.t === "ready") {
        readyRef.current = true;
        return;
      }
      if (msg.t === "error") {
        onStateRef.current?.(null);
        setFrameErr(msg.code === "empty" ? tr("view.drawio.empty") : tr("view.drawio.unreadable"));
        return;
      }
      setFrameErr("");
      onStateRef.current?.({ pages: msg.pages, page: msg.page, scale: msg.scale });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [tr]);

  // 描画要求。ready より先に送っても取りこぼされない（フレーム側が保持して load 後に
  // 流す）ので、ここでは XML とテーマが決まるたびに素直に送る。
  useEffect(() => {
    if (xml == null) return;
    const win = frameRef.current?.contentWindow;
    if (!win) return;
    win.postMessage({ af: DRAWIO_MSG, t: "render", xml, dark }, "*");
  }, [xml, dark]);

  if (err) return <pre className="filebody muted">({err})</pre>;

  return (
    <div className="drawioview">
      <iframe
        ref={frameRef}
        className="drawio-frame"
        title={tr("view.diagram")}
        // allow-same-origin も allow-popups も与えない: 前者はフレームを Console と
        // 同じ権限にしてしまい、後者は lightbox の window.open を許してしまう。
        sandbox="allow-scripts"
        srcDoc={srcdoc}
      />
      {xml == null && !err && <div className="drawio-note muted">…</div>}
      {frameErr && (
        <div className="drawio-note" role="status">
          {frameErr}
          {onShowSource && (
            <button type="button" onClick={onShowSource}>
              {tr("view.source")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
