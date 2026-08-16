// DrawioView — `.drawio` を図として表示する面（docs/65・ADR 0046）。
//
// 描画そのものは同梱の drawio ビューアが iframe の中で行う（drawioFrame.ts）。この
// コンポーネントの仕事は「取ってきて渡す」ことに尽きる。**フレームは何ひとつ自分で
// 取りに行かない**ので、取得はすべてここが行う:
//   1. 図の XML を **`api/fs/download` から** 取る。`api/fs/file` は 2 MiB で打ち切る
//      ので（maxEditorFileBytes）、画像を埋めた図がそこで「(file too large…)」に
//      化ける。download 側にサイズ上限は無い。
//   2. ビューア本体 4 MB の**ソース**を取る。**フレームに `<script src>` で読ませては
//      ならない** —— オリジンを持たないフレームからの要求は cross-site 扱いで
//      SameSite=Lax のセッション cookie が付かず、CP の authGate に 401 で弾かれる
//      （2026-08-16 の不具合。§65.11-7）。資格情報を持つ親が取り、本文を渡す。
//   3. フレームが `ready` と言ってから送る。iframe を作った直後に送ると、まだ srcdoc の
//      文書が無く、メッセージは初期の about:blank に配達されて消える（実測）。
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import viewerAssetUrl from "../../../vendor/drawio/viewer-static.min.js?url";
import { downloadURL } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { DRAWIO_MSG, drawioFrameSrcdoc, isDrawioFrameEvent } from "./drawioFrame.ts";

export interface DrawioState {
  pages: number;
  page: number;
  scale: number;
}

// ビューア本体はアプリで 1 回取れば足りる（ハッシュ付き資産なのでブラウザキャッシュも
// 効く）。失敗した promise は覚えない —— 次にペインを開いたときに再試行させる。
let viewerSourcePromise: Promise<string> | null = null;

function viewerSource(): Promise<string> {
  if (!viewerSourcePromise) {
    viewerSourcePromise = fetch(new URL(viewerAssetUrl, document.baseURI).href, {
      credentials: "same-origin",
    })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`viewer ${r.status}`))))
      .catch((e) => {
        viewerSourcePromise = null;
        throw e;
      });
  }
  return viewerSourcePromise;
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
  const [booted, setBooted] = useState(false);
  const [err, setErr] = useState("");
  const [frameErr, setFrameErr] = useState("");
  const onStateRef = useRef(onState);
  onStateRef.current = onState;

  // srcdoc は **一度だけ** 組み立てる。作り直すと iframe が再読み込みになり、ビューアを
  // もう一度評価することになるので、テーマ切り替えや別ファイルは postMessage 側で扱う。
  const srcdoc = useMemo(() => drawioFrameSrcdoc({ dark }), []); // eslint-disable-line react-hooks/exhaustive-deps

  const post = useCallback((message: Record<string, unknown>) => {
    const win = frameRef.current?.contentWindow;
    if (!win) return;
    win.postMessage({ af: DRAWIO_MSG, ...message }, "*");
  }, []);

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
        // 文書ができた合図。ここで初めてビューア本体を渡す。
        viewerSource()
          .then((src) => post({ t: "boot", src }))
          .catch(() => setFrameErr(tr("view.drawio.viewer_unavailable")));
        return;
      }
      if (msg.t === "booted") {
        setBooted(true);
        return;
      }
      if (msg.t === "error") {
        onStateRef.current?.(null);
        setFrameErr(
          msg.code === "boot"
            ? tr("view.drawio.viewer_unavailable")
            : msg.code === "empty"
              ? tr("view.drawio.empty")
              : tr("view.drawio.unreadable"),
        );
        return;
      }
      setFrameErr("");
      onStateRef.current?.({ pages: msg.pages, page: msg.page, scale: msg.scale });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [post, tr]);

  // 描画要求。ビューアが評価済みで XML が揃ってから送る。順番が逆でもフレーム側が
  // 1 通だけ保持するが、待てるならここで待つ方が経路が 1 本で済む。
  useEffect(() => {
    if (!booted || xml == null) return;
    post({ t: "render", xml, dark });
  }, [booted, xml, dark, post]);

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
      {xml == null && !err && !frameErr && <div className="drawio-note muted">…</div>}
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
