import { useEffect, useRef, useState } from "react";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { useLayoutStore } from "../../../layout/store.ts";
import { openSessionChatSplit } from "../../sessions/open.ts";
import { openChatSplit } from "../open.ts";
import { collectBlocks } from "../../mirror/turnTts.ts";

// StreamingMarkdown renders the live-accumulating reply as Markdown, throttled to one
// re-render per ~120ms — per-delta would re-parse and innerHTML-swap the whole bubble on
// every SSE chunk (killing text selection, wasting CPU). Trailing updates are always
// flushed, so the shown text never lags more than one window behind the stream.
const STREAM_RENDER_MS = 120;
export function StreamingMarkdown({ text, highlight }: { text: string; highlight?: string | null }) {
  const [shown, setShown] = useState(text);
  const lastRef = useRef(0); // when we last flushed
  const timerRef = useRef<number | null>(null);
  const textRef = useRef(text); // latest text, for the trailing flush
  textRef.current = text;
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const litRef = useRef<HTMLElement | null>(null);
  const litNeedleRef = useRef(""); // last highlighted sentence, so we scroll only when it changes
  useEffect(() => {
    const due = lastRef.current + STREAM_RENDER_MS;
    const now = Date.now();
    if (now >= due) {
      lastRef.current = now;
      setShown(text);
    } else if (timerRef.current == null) {
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null;
        lastRef.current = Date.now();
        setShown(textRef.current);
      }, due - now);
    }
  }, [text]);
  useEffect(
    () => () => {
      if (timerRef.current != null) clearTimeout(timerRef.current);
    },
    [],
  );
  // Live karaoke (docs/log/19): each ~120ms re-render rebuilds the bubble DOM and wipes any
  // highlight, so we (re)apply .tts-active after every render, driven by `highlight` (the
  // sentence the TTS just started). We locate the sentence's block by matching its
  // (whitespace-stripped) text against the rendered blocks — the same block set the
  // completed-turn karaoke walks (collectBlocks). Not found → keep the current highlight
  // (avoids flicker at sentence boundaries); no highlight → clear.
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    if (!highlight) {
      litRef.current?.classList.remove("tts-active");
      litRef.current = null;
      litNeedleRef.current = "";
      return;
    }
    const norm = (s: string) => s.replace(/\s+/g, "");
    const needle = norm(highlight).slice(0, 16);
    if (!needle) return;
    const target = collectBlocks(wrap).find((b) => norm(b.textContent || "").includes(needle));
    if (!target) return; // not found → keep the current highlight (no flicker at boundaries)
    // Re-apply the class every render (the DOM was rebuilt), but only scroll when the spoken
    // sentence actually changed — otherwise the ~120ms re-renders would spam smooth-scroll.
    if (litRef.current && litRef.current !== target) litRef.current.classList.remove("tts-active");
    target.classList.add("tts-active");
    litRef.current = target;
    if (litNeedleRef.current !== needle) {
      litNeedleRef.current = needle;
      target.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [shown, highlight]);
  useEffect(() => () => litRef.current?.classList.remove("tts-active"), []);
  return (
    <div ref={wrapRef}>
      <ChatMarkdown source={shown} breaks streaming />
    </div>
  );
}

export function ChatMarkdown({ source, breaks, streaming }: { source: string; breaks?: boolean; streaming?: boolean }) {
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  return (
    <MarkdownView
      source={source}
      breaks={breaks}
      streaming={streaming}
      onOpenFile={(path, line, column) =>
        openTargetInNew({ content: { kind: "file", filePath: path, targetLine: line, targetColumn: column } }, true)
      }
      // The chat lives in its own pane; opening a session or another conversation
      // in-place would replace it. Like file citations above, always open in a NEW pane.
      onOpenSession={(name) => openSessionChatSplit(name)}
      onOpenConversation={(id) => openChatSplit(id)}
    />
  );
}
