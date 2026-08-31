// DrawioView — `.drawio` を図として表示する面（docs/log/65・ADR 0046）。
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
//
// **テーマが変わったらフレームごと作り直す**（docs/log/65 §65.11-12）。同じフレームに
// 描き直しを頼んでも drawio のテーマは切り替わらない —— 実測では背景と塗りだけが
// 暗くなり、**コンテナ見出しが消え、エッジのラベルはライト時の白いピル＋黒文字のまま**
// 残った。色の決定は読み込み・初回描画の時点で固まる作りで、1 文書内でのテーマ往復は
// 想定されていない。作り直しの代償は 4MB の再評価（キャッシュから ~76ms）だけで、
// **見ていた場所（ページ・倍率・位置）は引き継ぐ**ので体感は連続する。
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import viewerAssetUrl from "../../../vendor/drawio/viewer-static.min.js?url";
import { downloadURL, rel } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { DRAWIO_MSG, drawioFrameSrcdoc, isDrawioFrameEvent, type DrawioViewState } from "./drawioFrame.ts";

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
  // 直近の現在地。フレームを作り直すときにそのまま渡す。
  const viewStateRef = useRef<DrawioViewState | null>(null);
  const [err, setErr] = useState("");
  const [frameErr, setFrameErr] = useState("");
  const onStateRef = useRef(onState);
  onStateRef.current = onState;

  // srcdoc はテーマごとに組み立てる。**テーマが変わったら作り直す**のが目的なので、
  // iframe には key を与えて React に新しい要素を作らせる（同じ要素の srcDoc を
  // 差し替える形だと、前の文書のリスナが残った window を掴み続けることがある）。
  const srcdoc = useMemo(() => drawioFrameSrcdoc({ dark }), [dark]);

  const post = useCallback((message: Record<string, unknown>) => {
    const win = frameRef.current?.contentWindow;
    if (!win) return;
    win.postMessage({ af: DRAWIO_MSG, ...message }, "*");
  }, []);

  // 新しいフレームは何も知らない状態から始まる。boot からやり直す。
  useEffect(() => {
    setBooted(false);
  }, [dark]);

  useEffect(() => {
    let alive = true;
    setXml(null);
    setErr("");
    setFrameErr("");
    // 別のファイルになったら現在地は捨てる（別の文書の座標は意味を持たない）。
    viewStateRef.current = null;
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
      if (msg.t === "stencils") {
        // フレームが申告したベンダーアイコンの図案を CP から取って渡す（docs/log/65 §65.5）。
        // **フレームには取らせない** —— オリジンが無いので cookie が付かず authGate に
        // 401 で弾かれる（§65.11-7 と同じ穴。実測済み）。
        //
        // 取れなかったものは黙って落とす: 閉域では図案だけが空になり、枠・色・ラベルは
        // 残る（＝ステンシルを持たなかった頃と同じ絵）。**エラー表示にしてはいけない** ——
        // 図は正しく開けているのだから、利用者に見せる異常ではない。
        // **URL は rel() で組み立てる。** 素の相対パスは文書 URL に対して解決されるので、
        // `/agent-fleet/` のようなパスを剥がすプロキシの下や `/open/...` の深い URL では
        // 行き先がずれる（§65.7 でビューア資産について記録したのと同じ罠）。
        Promise.all(
          msg.sets.map((name) =>
            fetch(rel(`api/drawio/stencils/${name.split("/").map(encodeURIComponent).join("/")}`), {
              credentials: "same-origin",
            })
              .then((r) => (r.ok ? r.text() : null))
              .catch(() => null),
          ),
        ).then((xmls) => {
          const got = xmls.filter((x): x is string => !!x);
          // **取れなかったものは名前を返す。** フレームが「頼んだ済み」から外して
          // 次の描画でもう一度頼めるようにするため —— 返さないと、upstream の 1 回の
          // 瞬断でそのペインの寿命いっぱいアイコンが欠ける（実機で reset を踏んだ）。
          const missing = msg.sets.filter((_, i) => !xmls[i]);
          if (got.length || missing.length) post({ t: "stencils", xml: got, missing });
        });
        return;
      }
      setFrameErr("");
      viewStateRef.current = {
        pageId: msg.pageId,
        scale: msg.scale,
        tx: msg.tx,
        ty: msg.ty,
        adjusted: msg.adjusted,
      };
      onStateRef.current?.({ pages: msg.pages, page: msg.page, scale: msg.scale });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [post, tr]);

  // 描画要求。ビューアが評価済みで XML が揃ってから送る。順番が逆でもフレーム側が
  // 1 通だけ保持するが、待てるならここで待つ方が経路が 1 本で済む。
  // 作り直し後は、直前に見ていた場所を一緒に渡して復元させる。
  useEffect(() => {
    if (!booted || xml == null) return;
    post({ t: "render", xml, dark, restore: viewStateRef.current });
  }, [booted, xml, dark, post]);

  if (err) return <pre className="filebody muted">({err})</pre>;

  return (
    <div className="drawioview">
      <iframe
        // テーマごとに別の要素にする（作り直しの契機はここ）。
        key={dark ? "dark" : "light"}
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
