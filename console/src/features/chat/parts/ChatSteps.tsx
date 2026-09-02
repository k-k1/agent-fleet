import { useRef, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { tCount, useT } from "../../../lib/i18n/index.ts";
// 作業過程はミラーの .mt-work をそのまま使っているので、最下部の「閉じる」も同じ部品を使う。
import { DisclosureFoot, revealHead } from "../../mirror/transcript/blocks.tsx";
import { ChatMarkdown } from "./ChatMarkdown.tsx";
import type { ChatStep } from "../../../types/chat.ts";

// ChatSteps renders an assistant turn's 作業過程 (docs/log/19 分離): the narration the model
// emitted before each tool call, kept separate from — but alongside — the final answer.
// Collapsible; open while streaming so progress is visible, collapsed once the turn is done.
export function ChatSteps({ steps, defaultOpen, live }: { steps: ChatStep[]; defaultOpen?: boolean; live?: boolean }) {
  const tr = useT();
  const [open, setOpen] = useState(!!defaultOpen);
  // 見出し（summary）。最下部の「閉じる」で畳んだあと、見出しが画面の上へ流れていたら戻す。
  const head = useRef<HTMLElement>(null);
  if (!steps.length) return null;
  const toolCount = steps.reduce((n, step) => n + (step.tools?.length ?? 0), 0);
  return (
    <details
      className={"mt-work chat-steps" + (live ? " live" : "")}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="mt-work-head" ref={head}>
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        {live && <Icon name="loading" spin />}
        <span className="mt-work-title">{tr("chat.work_process")}</span>
        <span className="mt-work-count muted">
          {tCount("chat.tool_count", toolCount)}
          {steps.length > 0 ? tCount("chat.interim_count", steps.length) : ""}
        </span>
      </summary>
      <div className="mt-work-body">
        {foldStepParts(steps).map((it, i) =>
          it.kind === "text" ? (
            <div key={i} className="chat-step">
              <ChatMarkdown source={it.text} breaks />
            </div>
          ) : (
            <ChatToolRun key={i} tools={it.tools} />
          ),
        )}
        <DisclosureFoot
          onClose={() => {
            setOpen(false);
            revealHead(head.current);
          }}
        />
      </div>
    </details>
  );
}

// Flatten a turn's 作業過程 into an ordered list of parts (narration text / tool name), then
// coalesce each maximal run of CONSECUTIVE tool calls into one folded run — matching
// MirrorView's foldParts/ToolRun. Narration between tools breaks a run (so a lone tool stays
// on its own; back-to-back tool-only steps fold together).
type StepItem = { kind: "text"; text: string } | { kind: "toolrun"; tools: string[] };
function foldStepParts(steps: ChatStep[]): StepItem[] {
  const items: StepItem[] = [];
  const pushTool = (name: string) => {
    const last = items[items.length - 1];
    if (last && last.kind === "toolrun") last.tools.push(name);
    else items.push({ kind: "toolrun", tools: [name] });
  };
  for (const s of steps) {
    const text = s.text?.trim();
    if (text) items.push({ kind: "text", text });
    for (const tool of s.tools ?? []) pushTool(tool);
  }
  return items;
}

// ChatToolRun renders a run of consecutive tool/mcp calls in the 作業過程. A lone call shows
// as a plain chip (as the mirror does for output-less traces); two or more fold into one
// collapsed "N 件のツール · tally" summary that expands to the individual calls. Mirrors
// MirrorView's ToolRun, reusing its .mt-toolrun / .mt-tool styling.
function ChatToolRun({ tools }: { tools: string[] }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  if (tools.length === 1) {
    return (
      <div className="mt-tool">
        <Icon name="tools" />
        <span className="mt-tool-name">{tools[0]}</span>
      </div>
    );
  }
  // Tally repeated names (Read×3 · Grep) so a long run reads at a glance.
  const tally: [string, number][] = [];
  const at: Record<string, number> = {};
  for (const name of tools) {
    if (at[name] === undefined) {
      at[name] = tally.length;
      tally.push([name, 0]);
    }
    tally[at[name]][1]++;
  }
  const summary = tally.map(([n, c]) => (c > 1 ? `${n}×${c}` : n)).join(" · ");
  return (
    <div className={"mt-toolrun" + (open ? " open" : "")}>
      <button
        type="button"
        className="mt-tool mt-toolrun-head"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={open ? tr("mirror.collapse_tools") : tr("mirror.expand_tools")}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="mt-tool-name">{tCount("mirror.tools_count", tools.length)}</span>
        <span className="mt-tool-info">{summary}</span>
      </button>
      {open && (
        <div className="mt-toolrun-body">
          {tools.map((name, i) => (
            <div key={i} className="mt-tool">
              <Icon name="tools" />
              <span className="mt-tool-name">{name}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
