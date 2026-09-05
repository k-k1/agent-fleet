import { useEffect, useRef, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { SelectionFloat } from "../../../ui/SelectionFloat.tsx";
import { prettyModel } from "../../../lib/modelName.ts";
import { t, useT } from "../../../lib/i18n/index.ts";
import { useSettings } from "../../../lib/settings.ts";
import { readTurn, collectBlocks, blockIndexAt, type TurnReadHandle } from "../../mirror/turnTts.ts";
import type { TtsOptions } from "../tts.ts";
import { ChatMarkdown } from "./ChatMarkdown.tsx";
import { ChatSteps } from "./ChatSteps.tsx";
import { ChatCopyButton } from "./ChatCopyButton.tsx";
import { formatMsgTS } from "./chatFormat.ts";
import type { ChatStep } from "../../../types/chat.ts";

// AssistantTurn renders one completed assistant reply and its footer. It owns a ref to the
// bubble body so the footer's read control can karaoke-read the RENDERED Markdown (docs/log/24):
// readTurn (features/mirror/turnTts) walks the .markdown DOM into blocks, speaks it sentence
// by sentence, and highlights the block whose sentence is playing (.tts-active) with scroll
// follow — the same engine the mirror/ReaderView use. Live streaming stays plain (the bubble
// re-renders every ~120ms, which would wipe any DOM highlight); karaoke is offered only once
// the turn is complete, i.e. here.
export function AssistantTurn({
  text,
  steps,
  ts,
  agentName,
  model,
  voice,
  highlight,
}: {
  text: string;
  steps?: ChatStep[];
  ts: number;
  agentName: string;
  model?: string;
  voice?: Partial<TtsOptions>;
  highlight?: string | null;
}) {
  const ttsEnabled = useSettings().ttsEnabled;
  const tr = useT();
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const handleRef = useRef<TurnReadHandle | null>(null);
  const [state, setState] = useState<"idle" | "playing" | "paused">("idle");
  // Floating "read from here" pill anchored to a mouse selection inside the bubble.
  const [selPill, setSelPill] = useState<{ x: number; y: number; block: number } | null>(null);
  const autoLitRef = useRef<HTMLElement | null>(null);

  // Auto-reading moves to this finished DOM once the final answer settles, so map the
  // sentence reported by startTts onto a body block and light it up.
  useEffect(() => {
    const body = bodyRef.current;
    if (!body || !highlight) {
      autoLitRef.current?.classList.remove("tts-active");
      autoLitRef.current = null;
      return;
    }
    const norm = (s: string) => s.replace(/\s+/g, "");
    const needle = norm(highlight).slice(0, 16);
    if (!needle) return;
    const target = collectBlocks(body).find((b) => norm(b.textContent || "").includes(needle));
    if (!target) return;
    if (autoLitRef.current && autoLitRef.current !== target) autoLitRef.current.classList.remove("tts-active");
    target.classList.add("tts-active");
    autoLitRef.current = target;
    target.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [highlight]);
  useEffect(() => () => autoLitRef.current?.classList.remove("tts-active"), []);

  // Stop this bubble's reading if the pane/component goes away mid-read.
  useEffect(() => () => handleRef.current?.stop("replaced"), []);

  const start = (fromBlock: number) => {
    const body = bodyRef.current;
    if (!body) return;
    handleRef.current?.stop("replaced");
    // onEnd fires once on natural end AND on preemption (TopBar stop / another playback),
    // so the footer always falls back to the idle "read aloud" state.
    const h = readTurn(body, t("chat.label"), fromBlock, () => {
      handleRef.current = null;
      setState("idle");
    }, voice);
    if (h) {
      handleRef.current = h;
      setState("playing");
    }
  };
  const pause = () => {
    handleRef.current?.pause();
    setState("paused");
  };
  const resume = () => {
    handleRef.current?.resume();
    setState("playing");
  };
  const stop = () => {
    handleRef.current?.stop();
    handleRef.current = null;
    setState("idle");
  };

  // After a mouse selection inside the bubble, surface a "read from here" pill at the
  // selection head — reading (re)starts from the block the selection begins in. Desktop
  // mouse only (touch selection emits no mouseup); the footer button still reads from top.
  const onMouseUp = () => {
    const body = bodyRef.current;
    const sel = window.getSelection();
    if (!ttsEnabled || !body || !sel || sel.isCollapsed || sel.rangeCount === 0) {
      setSelPill(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!body.contains(range.startContainer)) {
      setSelPill(null);
      return;
    }
    const idx = blockIndexAt(collectBlocks(body), range.startContainer);
    if (idx < 0) {
      setSelPill(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    setSelPill({ x: Math.round(rect.left), y: Math.round(rect.top - 34), block: idx });
  };
  const startFromSelection = () => {
    if (!selPill) return;
    start(selPill.block);
    setSelPill(null);
    window.getSelection()?.removeAllRanges();
  };

  return (
    <>
      <div className="chat-role">
        {agentName}
        {/* The model that answered, faint beside the agent name — the mirror's turn
            header contract (.mt-model) applied to the chat. Recorded per turn, so a
            thread that changed model (or fell back to another backend) shows what each
            reply was actually produced with. title keeps the raw id copyable. */}
        {model && (
          <span className="chat-model" title={model}>
            {prettyModel(model)}
          </span>
        )}
      </div>
      {/* The work trace (tool responses) sits collapsed above the final answer; collapsed by
          default and kept, not discarded. */}
      {steps && steps.length > 0 && <ChatSteps steps={steps} />}
      <div className="chat-body" ref={bodyRef} onMouseUp={onMouseUp}>
        {text && <ChatMarkdown source={text} breaks />}
      </div>
      {selPill && (
        <SelectionFloat x={selPill.x} y={selPill.y} className="sel-pill-group">
          <button
            type="button"
            className="sel-send-pill"
            onMouseDown={(e) => e.preventDefault()}
            onClick={startFromSelection}
          >
            <Icon name="unmute" /> {tr("chat.read_from_here")}
          </button>
        </SelectionFloat>
      )}
      <div className="chat-msg-foot">
        {ts > 0 && <span className="cm-time">{formatMsgTS(ts)}</span>}
        {ttsEnabled && text.trim() && (
          state === "idle" ? (
            <button type="button" className="ghost cm-copy" title={tr("chat.read_title")} onClick={() => start(0)}>
              <Icon name="unmute" /> {tr("chat.read")}
            </button>
          ) : (
            <>
              {state === "playing" ? (
                <button type="button" className="ghost cm-copy" title={tr("chat.pause")} onClick={pause}>
                  <Icon name="debug-pause" /> {tr("chat.pause")}
                </button>
              ) : (
                <button type="button" className="ghost cm-copy" title={tr("chat.resume")} onClick={resume}>
                  <Icon name="play" /> {tr("chat.resume")}
                </button>
              )}
              <button type="button" className="ghost cm-copy" title={tr("chat.stop")} onClick={stop}>
                <Icon name="debug-stop" /> {tr("chat.stop")}
              </button>
            </>
          )
        )}
        <ChatCopyButton text={text} />
      </div>
    </>
  );
}
