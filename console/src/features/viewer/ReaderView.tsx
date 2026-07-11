// ReaderView — 朗読ビュー（docs/24）。ファイル本文を「読む」ためのビュー。本文を段落・文に
// 分割して読みやすい版組で表示し、冒頭から順次読み上げ（TTS）しながら、いま読んでいる文を
// カラオケ・ハイライト＋自動スクロールで追従する。縦書き/横書きを切り替えられる。
//
// 読み上げエンジンは features/chat/tts.ts の startNarration を流用（文配列を渡し、再生開始した
// 文の index を onUnit で受けてハイライトする）。グローバル 1 本再生・TopBar 停止と相乗り。
// content kind は "read"（layout/types.ts）。ファイル右クリック「朗読で開く」や FileView の
// 「朗読」ボタンから開く。
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api } from "../../core/api/client.ts";
import { baseName, langFor } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, setSetting } from "../../lib/settings.ts";
import { startNarration, BLOCK_BEAT, readerVoiceChoices, voiceChoiceOpts, type NarrationHandle } from "../chat/tts.ts";
import { effectiveDict } from "../chat/ttsDict.ts";
import { loadSpeakers } from "../chat/ttsSpeakers.ts";
import { buildReadUnits } from "./readerText.ts";

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
  // 選択範囲から朗読を（再）開始するピル（選択の先頭にある読み上げ単位の index と表示位置）。
  const [selPill, setSelPill] = useState<{ x: number; y: number; idx: number } | null>(null);
  // 声セレクトの選択肢はキャラクター設定×エンジン実カタログ（tts.ts の readerVoiceChoices）。
  // カタログは非同期取得なので、届いたら再レンダして静的フォールバックから差し替える。
  const [, setCatalogLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setCatalogLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  const voiceChoices = readerVoiceChoices();
  // 保存済みの声が選択肢に無い（キャラを無効化した・基準スタイルを変えた等）→「設定の話者」
  // として扱う（表示と実再生を一致させる）。
  const readerVoice = voiceChoices.some(([v]) => v === settings.readerVoice) ? settings.readerVoice : "";

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
    setSelPill(null);
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
  // 原文忠実（改行・行頭スペース保持）＋なろう形式ルビの表示単位。インラインコードの
  // 省略読み（abbrevCode）は読み上げテキスト側にだけ効く（表示は原文のまま）。辞書は
  // ユーザー＋テナント共通の合成（ユーザー優先）。
  const codeOpts = useMemo(
    () => ({ abbrev: settings.ttsAbbrevCode, dict: effectiveDict() }),
    [settings.ttsAbbrevCode, settings.ttsUserDict],
  );
  const units = useMemo(
    () => (isText ? buildReadUnits(data!.content!, isMarkdown, codeOpts) : []),
    [isText, data, isMarkdown, codeOpts],
  );
  // 読み上げ対象（spoken 非空）の単位に連番を振る。data-si=その連番、active と一致でハイライト。
  const spokenIdx = useMemo(() => {
    const m: (number | null)[] = [];
    let n = 0;
    for (const u of units) m.push(u.spoken ? n++ : null);
    return m;
  }, [units]);
  const flat = useMemo(() => units.filter((u) => u.spoken).map((u) => u.spoken), [units]);
  // 読み上げ単位ごとの前拍（リスト・見出し・引用の頭で一拍）。flat と同じ並び。
  const flatPre = useMemo(() => units.filter((u) => u.spoken).map((u) => (u.preBeat ? BLOCK_BEAT : 0)), [units]);

  const vertical = settings.readerVertical;
  const ttsOn = settings.ttsEnabled;
  const verticalRef = useRef(vertical);
  verticalRef.current = vertical;

  // 縦書き（vertical-rl）は横スクロールで読み進める。ホイール↓（deltaY>0）を「←へ」
  // ＝後続の列（左）へ変換する。React の onWheel は passive 登録で preventDefault が効かない
  // ため、ネイティブ wheel を passive:false で張る。本文 DOM の有無で張り直す。
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if (!verticalRef.current || e.deltaY === 0) return;
      el.scrollLeft -= e.deltaY; // spec準拠ブラウザ: 後続列は scrollLeft 負方向
      e.preventDefault();
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [isText, units.length]);

  // いま読んでいる文をビューへ。block=論理ブロック軸なので縦書き（vertical-rl）でも
  // 横スクロールで正しく追従する。
  useEffect(() => {
    if (active == null) return;
    scrollRef.current?.querySelector<HTMLElement>(`[data-si="${active}"]`)?.scrollIntoView({
      block: "center",
      inline: "nearest",
      behavior: "smooth",
    });
  }, [active]);

  // アンマウントで朗読停止（別ファイルを開く等で本文 DOM が消えるため）。
  useEffect(() => () => handleRef.current?.stop(), []);

  // from 番目の読み上げ単位から（再）開始。ハイライトは from を足してフル配列基準に戻す。
  // voice は再生中の声変更（下のセレクト onChange）用 — setSetting 直後は settings の
  // クロージャが古いので、新しい選択値から組んだ上書きを直接渡す。
  const startFrom = (from: number, voice = voiceChoiceOpts(readerVoice)) => {
    const slice = flat.slice(from);
    if (!slice.length) return;
    handleRef.current?.stop();
    const h = startNarration(
      slice,
      baseName(filePath),
      (i) => {
        setActive(i == null ? null : i + from);
        if (i == null) {
          // 自然終了 or 外部（TopBar 停止・他再生開始）で終了
          setReading(false);
          setPaused(false);
          handleRef.current = null;
        }
      },
      voice, // ヘッダーで選んだ声（"" = 設定の話者）
      flatPre.slice(from),
    );
    handleRef.current = h;
    setReading(true);
    setPaused(false);
  };
  const start = () => startFrom(0);
  const stop = () => handleRef.current?.stop();

  // 選択の開始ノードから、その位置（以降）の読み上げ単位の index を得る。選択が非読み上げ単位
  // （空行/区切り）に始まるときは後続の最初の読み上げ単位へ送る。無ければ null。
  const spokenIdxAt = (node: Node): number | null => {
    const el = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
    const unit = el?.closest<HTMLElement>(".reader-unit");
    if (!unit) return null;
    if (unit.dataset.si != null) return Number(unit.dataset.si);
    for (let n = unit.nextElementSibling; n; n = n.nextElementSibling) {
      const si = (n as HTMLElement).dataset?.si;
      if (si != null) return Number(si);
    }
    return null;
  };

  // 本文内で選択が確定したら、選択の先頭に「ここから朗読」ピルを出す（再生中でも再スタート可）。
  const captureSelection = () => {
    const sel = window.getSelection();
    const body = scrollRef.current;
    if (!ttsOn || !sel || sel.isCollapsed || sel.rangeCount === 0 || !body) {
      setSelPill(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!body.contains(range.startContainer)) {
      setSelPill(null);
      return;
    }
    const idx = spokenIdxAt(range.startContainer);
    if (idx == null) {
      setSelPill(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    setSelPill({ x: Math.round(rect.left), y: Math.round(rect.top - 34), idx });
  };
  const startFromSelection = () => {
    if (!selPill) return;
    startFrom(selPill.idx);
    setSelPill(null);
    window.getSelection()?.removeAllRanges();
  };

  // タッチ選択（長押し＋ドラッグ）は mouseup を出さないので、selectionchange でもピルを更新
  // する。連続発火するのでデバウンス。最新クロージャを ref 経由で呼ぶ（mount-once の effect）。
  const captureRef = useRef(captureSelection);
  captureRef.current = captureSelection;
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | null = null;
    const onSelChange = () => {
      if (t) clearTimeout(t);
      t = setTimeout(() => captureRef.current(), 250);
    };
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      if (t) clearTimeout(t);
    };
  }, []);
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
        {isText && (
          <select
            className="reader-voice"
            value={readerVoice}
            onChange={(e) => {
              const v = e.target.value;
              setSetting("readerVoice", v);
              // 朗読中の変更は、いま読んでいる文から新しい声で読み直す（続きから声が切り替わる）。
              if (reading) startFrom(active ?? 0, voiceChoiceOpts(v));
            }}
            disabled={!ttsOn}
            title={ttsOn ? "朗読の声（朗読中に変えると続きから切り替え）" : "設定で音声読み上げを有効にしてください"}
          >
            {voiceChoices.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
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
        <div className={"reader-body" + (vertical ? " vertical" : "")} ref={scrollRef} onMouseUp={captureSelection}>
          {units.map((u, ui) => {
            const si = spokenIdx[ui];
            return (
              <span
                key={ui}
                data-si={si ?? undefined}
                className={"reader-unit" + (si != null && si === active ? " active" : "")}
              >
                {u.segs.map((s, j) =>
                  s.ruby !== undefined ? (
                    <ruby key={j}>
                      {s.base}
                      <rt>{s.ruby}</rt>
                    </ruby>
                  ) : (
                    s.base
                  ),
                )}
              </span>
            );
          })}
        </div>
      )}
      {selPill &&
        createPortal(
          <div className="sel-pill-group" style={{ left: selPill.x, top: Math.max(4, selPill.y) }}>
            <button type="button" className="sel-send-pill" onMouseDown={(e) => e.preventDefault()} onClick={startFromSelection}>
              <Icon name="unmute" /> ここから朗読
            </button>
          </div>,
          document.body,
        )}
    </div>
  );
}
