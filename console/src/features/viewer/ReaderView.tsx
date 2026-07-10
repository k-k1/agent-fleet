// ReaderView — 朗読ビュー（docs/24）。ファイル本文を「読む」ためのビュー。本文を段落・文に
// 分割して読みやすい版組で表示し、冒頭から順次読み上げ（TTS）しながら、いま読んでいる文を
// カラオケ・ハイライト＋自動スクロールで追従する。縦書き/横書きを切り替えられる。
//
// 読み上げエンジンは features/chat/tts.ts の startNarration を流用（文配列を渡し、再生開始した
// 文の index を onUnit で受けてハイライトする）。グローバル 1 本再生・TopBar 停止と相乗り。
// content kind は "read"（layout/types.ts）。ファイル右クリック「朗読で開く」や FileView の
// 「朗読」ボタンから開く。
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { baseName, langFor } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, setSetting } from "../../lib/settings.ts";
import { startNarration, type NarrationHandle } from "../chat/tts.ts";
import { toReadingParagraphs } from "./readerText.ts";

interface FileData {
  error?: { message?: string };
  binary?: boolean;
  content?: string;
}

export function ReaderView({ filePath }: { filePath: string }) {
  const settings = useSettings();
  const [data, setData] = useState<FileData | null>(null);
  const [err, setErr] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const handleRef = useRef<NarrationHandle | null>(null);
  const [reading, setReading] = useState(false);
  const [paused, setPaused] = useState(false);
  const [active, setActive] = useState<number | null>(null);

  // 本文取得（FileView と同じ /api/fs/file）。ファイルが変わったら朗読を止めてリセット。
  useEffect(() => {
    if (!filePath) return;
    let alive = true;
    handleRef.current?.stop();
    setData(null);
    setErr("");
    setActive(null);
    setReading(false);
    setPaused(false);
    api(`api/fs/file?path=${encodeURIComponent(filePath)}`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) setErr(d.error.message || "読み込めません");
        else setData(d);
      })
      .catch(() => alive && setErr("読み込めません"));
    return () => {
      alive = false;
    };
  }, [filePath]);

  const isText = !!data && !data.binary && typeof data.content === "string";
  const isMarkdown = isText && langFor(filePath) === "markdown";
  const paras = useMemo(() => (isText ? toReadingParagraphs(data!.content!, isMarkdown) : []), [isText, data, isMarkdown]);
  const flat = useMemo(() => paras.flat(), [paras]);
  // 段落 → その先頭文のフラット index（文 span に一意の data-ri を振るため）。
  const offsets = useMemo(() => {
    const o: number[] = [];
    let n = 0;
    for (const p of paras) {
      o.push(n);
      n += p.length;
    }
    return o;
  }, [paras]);

  const vertical = settings.readerVertical;
  const ttsOn = settings.ttsEnabled;

  // いま読んでいる文をビューへ。block=論理ブロック軸なので縦書き（vertical-rl）でも
  // 横スクロールで正しく追従する。
  useEffect(() => {
    if (active == null) return;
    scrollRef.current?.querySelector<HTMLElement>(`[data-ri="${active}"]`)?.scrollIntoView({
      block: "center",
      inline: "nearest",
      behavior: "smooth",
    });
  }, [active]);

  // アンマウントで朗読停止（別ファイルを開く等で本文 DOM が消えるため）。
  useEffect(() => () => handleRef.current?.stop(), []);

  const start = () => {
    if (!flat.length) return;
    const h = startNarration(flat, baseName(filePath), (i) => {
      setActive(i);
      if (i == null) {
        // 自然終了 or 外部（TopBar 停止・他再生開始）で終了
        setReading(false);
        setPaused(false);
        handleRef.current = null;
      }
    });
    handleRef.current = h;
    setReading(true);
    setPaused(false);
  };
  const stop = () => handleRef.current?.stop();
  const togglePause = () => {
    const h = handleRef.current;
    if (!h) return;
    if (h.isPaused()) {
      h.resume();
      setPaused(false);
    } else {
      h.pause();
      setPaused(true);
    }
  };

  return (
    <div className="fileview readerview">
      <header className="view-head fileinfo reader-head">
        <span className="fi-name mono">
          <FileIcon name={baseName(filePath)} /> {baseName(filePath)}
        </span>
        {isText && <span className="fi-meta muted">{flat.length} 文</span>}
        {isText && (
          <span className="ui-seg sm md-toggle">
            {!reading ? (
              <button
                type="button"
                className="seg-btn"
                onClick={start}
                disabled={!ttsOn || !flat.length}
                title={ttsOn ? "冒頭から朗読" : "設定で音声読み上げを有効にしてください"}
              >
                <Icon name="unmute" /> 朗読
              </button>
            ) : (
              <>
                <button type="button" className="seg-btn" onClick={togglePause} title={paused ? "再開" : "一時停止"}>
                  <Icon name={paused ? "play" : "debug-pause"} /> {paused ? "再開" : "一時停止"}
                </button>
                <button type="button" className="seg-btn active" onClick={stop} title="朗読を停止">
                  <Icon name="debug-stop" /> 停止
                </button>
              </>
            )}
          </span>
        )}
        <span className="ui-seg sm md-toggle">
          <button
            type="button"
            className={"seg-btn" + (vertical ? " active" : "")}
            onClick={() => setSetting("readerVertical", !vertical)}
            title={vertical ? "横書きに切り替え" : "縦書きに切り替え"}
          >
            <Icon name="book" /> {vertical ? "横書き" : "縦書き"}
          </button>
        </span>
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
      </header>

      {err ? (
        <pre className="filebody muted">({err})</pre>
      ) : data == null ? (
        <pre className="filebody muted">…</pre>
      ) : !isText ? (
        <pre className="filebody muted">(このファイルは朗読できません)</pre>
      ) : !flat.length ? (
        <pre className="filebody muted">(読み上げる本文がありません)</pre>
      ) : (
        <div className={"reader-body" + (vertical ? " vertical" : "")} ref={scrollRef}>
          {paras.map((sents, pi) => (
            <p className="reader-para" key={pi}>
              {sents.map((s, si) => {
                const idx = offsets[pi] + si;
                return (
                  <span key={si} data-ri={idx} className={"reader-sent" + (idx === active ? " active" : "")}>
                    {s}{" "}
                  </span>
                );
              })}
            </p>
          ))}
        </div>
      )}
    </div>
  );
}
