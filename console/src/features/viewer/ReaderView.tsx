// ReaderView — the reader view (docs/log/24), for reading a file's prose. It splits the body
// into paragraphs and sentences, typesets them for reading, speaks them in order (TTS) and
// follows the current sentence with a karaoke highlight plus auto-scroll. Vertical and
// horizontal writing can be toggled.
//
// Speech reuses startNarration from features/chat/tts.ts: it takes the array of sentences and
// reports the index of the sentence that started playing through onUnit, which drives the
// highlight. It shares the single global playback and the TopBar stop control.
// The content kind is "read" (layout/types.ts). Opened from the file context menu's "open in the
// reader" (「朗読で開く」) or FileView's reader button (「朗読」).
import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { baseName, langFor } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { SelectionFloat } from "../../ui/SelectionFloat.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useSelectionCapture } from "../../lib/selectionCapture.ts";
import { useSettings, setSetting, READER_FONTS, readerFontStack } from "../../lib/settings.ts";
import { useLocale, useT } from "../../lib/i18n/index.ts";
import { startNarration, BLOCK_BEAT, SENT_BEAT, TAME_BEAT, readerVoiceChoices, voiceChoiceOpts, type NarrationHandle } from "../chat/tts.ts";
import { effectiveDict } from "../chat/ttsDict.ts";
import { loadSpeakers } from "../chat/ttsSpeakers.ts";
import { splitLongSentence } from "../chat/ttsText.ts";
import { buildReadUnits, readPreGaps } from "./readerText.ts";

interface FileData {
  error?: { message?: string };
  binary?: boolean;
  content?: string;
}

export function ReaderView({ filePath, headerActions }: { filePath: string; headerActions?: ReactNode }) {
  const tr = useT();
  const settings = useSettings();
  // Narou-style ruby and vertical writing are Japanese-only features, so they are enabled only
  // when the UI locale is ja; otherwise ruby parsing is off and the vertical toggle is hidden
  // (docs/log/28 §2.4).
  const ja = useLocale() === "ja";
  const [data, setData] = useState<FileData | null>(null);
  const [err, setErr] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const handleRef = useRef<NarrationHandle | null>(null);
  const [reading, setReading] = useState(false);
  const [paused, setPaused] = useState(false);
  const [active, setActive] = useState<number | null>(null);
  // The pill that (re)starts reading from the selection: the index of the speech unit at the
  // start of the selection, plus where to draw it.
  const [selPill, setSelPill] = useState<{ x: number; y: number; idx: number } | null>(null);
  // The voice select's options are the character settings crossed with the engine's real
  // catalogue (readerVoiceChoices in tts.ts). The catalogue is fetched asynchronously, so a
  // re-render on arrival replaces the static fallback.
  const [, setCatalogLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setCatalogLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  const voiceChoices = readerVoiceChoices();
  // A saved voice that is no longer among the options (the character was disabled, the base
  // style changed, …) falls back to "the speaker from settings", so what is shown and what is
  // actually played agree.
  const readerVoice = voiceChoices.some(([v]) => v === settings.readerVoice) ? settings.readerVoice : "";

  // Fetches the body from the same /api/fs/file as FileView, stopping and resetting the reading
  // when the file changes. Right after a workspace starts the agent is briefly unreachable and
  // api() returns http_5xx rather than throwing, so transient failures are retried with backoff
  // (isTransientErr) and only permanent errors are shown.
  useRetryLoad(async (signal) => {
    if (!filePath) return true;
    handleRef.current?.stop("replaced");
    setData(null);
    setErr("");
    setActive(null);
    setReading(false);
    setPaused(false);
    setSelPill(null);
    let d;
    try {
      d = await api(`api/fs/file?path=${encodeURIComponent(filePath)}`);
    } catch {
      return false; // network drop — retry
    }
    if (signal.aborted) return true;
    if (isTransientErr(d)) return false;
    if (d && d.error) setErr(d.error.message || tr("view.cannot_load"));
    else setData(d);
    return true;
  }, [filePath]);

  const isText = !!data && !data.binary && typeof data.content === "string";
  const isMarkdown = isText && langFor(filePath) === "markdown";
  // Display units faithful to the source (newlines and leading spaces preserved) plus
  // Narou-style ruby. Abbreviated reading of inline code (abbrevCode) affects only the spoken
  // text; the display stays as written. The dictionary merges the user's and the tenant's, with
  // the user's taking precedence.
  const codeOpts = useMemo(
    () => ({ abbrev: settings.ttsAbbrevCode, dict: effectiveDict() }),
    [settings.ttsAbbrevCode, settings.ttsUserDict],
  );
  const units = useMemo(
    () => (isText ? buildReadUnits(data!.content!, isMarkdown, codeOpts, ja) : []),
    [isText, data, isMarkdown, codeOpts, ja],
  );
  // Numbers the units that are actually spoken (non-empty `spoken`) in sequence. data-si holds
  // that number, and a unit is highlighted when it matches `active`.
  const spokenIdx = useMemo(() => {
    const m: (number | null)[] = [];
    let n = 0;
    for (const u of units) m.push(u.spoken ? n++ : null);
    return m;
  }, [units]);
  const flat = useMemo(() => units.filter((u) => u.spoken).map((u) => u.spoken), [units]);
  // The lead-in beat per speech unit: a full beat at the head of a paragraph or marker line, a
  // short beat at a full stop within a line, none at a hard wrap. Same order as `flat`.
  const flatPre = useMemo(() => readPreGaps(units, BLOCK_BEAT, SENT_BEAT, TAME_BEAT), [units]);
  // A long sentence is split further for synthesis, so waiting on the synthesiser does not leave
  // silence. origOf maps a piece back to its original speech (highlight) unit, and head marks
  // the first piece of a sentence, which is the only one that gets the lead-in beat.
  const split = useMemo(() => {
    const texts: string[] = [];
    const origOf: number[] = [];
    const head: boolean[] = [];
    flat.forEach((s, i) => {
      splitLongSentence(s).forEach((piece, j) => {
        texts.push(piece);
        origOf.push(i);
        head.push(j === 0);
      });
    });
    return { texts, origOf, head };
  }, [flat]);

  const vertical = ja && settings.readerVertical; // vertical writing is ja-only; others stay horizontal
  const ttsOn = settings.ttsEnabled;
  const verticalRef = useRef(vertical);
  verticalRef.current = vertical;

  // Vertical writing (vertical-rl) progresses by scrolling horizontally, so a downward wheel
  // (deltaY > 0) is translated to leftward, i.e. towards the following columns. React's onWheel
  // registers passive, where preventDefault does nothing, so bind a native wheel listener with
  // passive:false, rebinding whenever the body DOM appears or disappears.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if (!verticalRef.current || e.deltaY === 0) return;
      el.scrollLeft -= e.deltaY; // in a spec-compliant browser the following columns are negative scrollLeft
      e.preventDefault();
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [isText, units.length]);

  // Brings the sentence being read into view. `block` is the logical block axis, so this follows
  // correctly by scrolling horizontally in vertical writing (vertical-rl) too.
  useEffect(() => {
    if (active == null) return;
    scrollRef.current?.querySelector<HTMLElement>(`[data-si="${active}"]`)?.scrollIntoView({
      block: "center",
      inline: "nearest",
      behavior: "smooth",
    });
  }, [active]);

  // Stop reading on unmount: the body DOM disappears when another file is opened.
  useEffect(() => () => handleRef.current?.stop("replaced"), []);

  // (Re)starts from speech unit `from`. Synthesis runs on the split texts, and the highlight is
  // mapped back to the original speech unit through origOf.
  const startFrom = (from: number, voice = voiceChoiceOpts(readerVoice)) => {
    const start = split.origOf.findIndex((o) => o >= from);
    if (start < 0) return;
    const slice = split.texts.slice(start);
    if (!slice.length) return;
    // Lead-in beat: the first piece of a sentence keeps the original unit's beat; the
    // continuation pieces of a synthesis split get none.
    const pres = slice.map((_, k) => (split.head[start + k] ? flatPre[split.origOf[start + k]] : 0));
    handleRef.current?.stop("replaced");
    const h = startNarration(
      slice,
      baseName(filePath),
      (i) => {
        setActive(i == null ? null : split.origOf[start + i]);
        if (i == null) {
          // Ended, either naturally or externally (TopBar stop, another playback starting)
          setReading(false);
          setPaused(false);
          handleRef.current = null;
        }
      },
      voice, // the voice chosen in the header ("" = the speaker from settings)
      pres,
    );
    handleRef.current = h;
    setReading(true);
    setPaused(false);
  };
  const start = () => startFrom(0);
  const stop = () => handleRef.current?.stop();

  // Given the selection's start node, returns the index of the speech unit at or after that
  // position. When the selection starts on a non-spoken unit (a blank line or a separator) it
  // moves on to the next spoken unit; null if there is none.
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

  // Once a selection settles inside the body, show the "read from here" pill at its start; it
  // can restart the reading even while playback is running.
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

  // Touch selection (long press and drag) emits no mouseup, so the pill is also updated from
  // selectionchange (lib/selectionCapture).
  useSelectionCapture(captureSelection);
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

  // The body font and size are passed to .reader-body as CSS variables, read by viewer.css.
  const readerStyle = {
    "--reader-font": readerFontStack(settings.readerFont),
    "--reader-size": settings.readerSize + "px",
  } as CSSProperties;

  return (
    <div className="fileview readerview" style={readerStyle}>
      <ViewHead className="fileinfo reader-head" actions={headerActions}>
        <span className="fi-name mono">
          <FileIcon name={baseName(filePath)} /> {baseName(filePath)}
        </span>
        {isText && <span className="fi-meta muted">{flat.length} {tr("view.sentences")}</span>}
        {isText && (
          <span className="ui-seg sm md-toggle">
            {!reading ? (
              <button
                type="button"
                className="seg-btn"
                onClick={start}
                disabled={!ttsOn || !flat.length}
                title={ttsOn ? tr("view.read_from_start") : tr("view.enable_tts_tip")}
              >
                <Icon name="unmute" /> {tr("view.read_aloud")}
              </button>
            ) : (
              <>
                <button type="button" className="seg-btn" onClick={togglePause} title={paused ? tr("view.resume") : tr("view.pause")}>
                  <Icon name={paused ? "play" : "debug-pause"} /> {paused ? tr("view.resume") : tr("view.pause")}
                </button>
                <button type="button" className="seg-btn active" onClick={stop} title={tr("view.stop_reading")}>
                  <Icon name="debug-stop" /> {tr("view.stop")}
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
              // Changing it mid-reading does not interrupt: the current sentence finishes and
              // the new voice takes over from the next one.
              handleRef.current?.setVoice(voiceChoiceOpts(v));
            }}
            disabled={!ttsOn}
            title={ttsOn ? tr("view.reader_voice_tip") : tr("view.enable_tts_tip")}
          >
            {voiceChoices.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
        )}
        {ja && (
          <span className="ui-seg sm md-toggle">
            <button
              type="button"
              className={"seg-btn" + (vertical ? " active" : "")}
              onClick={() => setSetting("readerVertical", !vertical)}
              title={vertical ? tr("view.switch_horizontal") : tr("view.switch_vertical")}
            >
              <Icon name="book" /> {vertical ? tr("view.horizontal") : tr("view.vertical")}
            </button>
          </span>
        )}
        <select
          className="reader-voice reader-font"
          value={settings.readerFont}
          onChange={(e) => setSetting("readerFont", e.target.value)}
          title={tr("view.body_font")}
          style={{ fontFamily: readerFontStack(settings.readerFont) }}
        >
          {READER_FONTS.map((f) => (
            <option key={f} value={f} style={{ fontFamily: readerFontStack(f) }}>
              {f}
            </option>
          ))}
        </select>
        <span className="ui-seg sm md-toggle reader-size">
          <button
            type="button"
            className="seg-btn"
            onClick={() => setSetting("readerSize", Math.max(9, settings.readerSize - 1))}
            disabled={settings.readerSize <= 9}
            title={tr("view.smaller_text")}
          >
            <Icon name="dash" />
          </button>
          <span className="seg-btn reader-size-val" title={tr("view.body_font_size")}>
            {settings.readerSize}
          </span>
          <button
            type="button"
            className="seg-btn"
            onClick={() => setSetting("readerSize", Math.min(28, settings.readerSize + 1))}
            disabled={settings.readerSize >= 28}
            title={tr("view.larger_text")}
          >
            <Icon name="add" />
          </button>
        </span>
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
      </ViewHead>

      {err ? (
        <pre className="filebody muted">({err})</pre>
      ) : data == null ? (
        <pre className="filebody muted">…</pre>
      ) : !isText ? (
        <pre className="filebody muted">{tr("view.cannot_read_file")}</pre>
      ) : !flat.length ? (
        <pre className="filebody muted">{tr("view.no_text_to_read")}</pre>
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
          <SelectionFloat x={selPill.x} y={selPill.y} className="sel-pill-group">
            <button type="button" className="sel-send-pill" onMouseDown={(e) => e.preventDefault()} onClick={startFromSelection}>
              <Icon name="unmute" /> {tr("view.read_from_here")}
            </button>
          </SelectionFloat>,
          document.body,
        )}
    </div>
  );
}
