import { fmtTok } from "../lib/fmttok.js";
import Icon from "./Icon.jsx";

// contextWindow returns the model's context length. The current Claude family
// (Opus 4.6/4.7/4.8, Sonnet 4.6, Fable/Mythos 5) is 1M-native — 1M is the default
// window, not a 200k default you grow into. Haiku is 200k. Unknown/older models
// assume 200k but grow to fit if a 1M beta is clearly in use.
export function contextWindow(model: string | null | undefined, used: number): number {
  const m = (model || "").toLowerCase();
  if (/opus-4-[678]|sonnet-4-6|fable-5|mythos-5/.test(m)) return 1000000;
  if (/haiku/.test(m)) return 200000;
  return used > 200000 ? 1000000 : 200000;
}

interface ContextBarProps {
  read: number;
  create: number;
  fresh: number;
  model?: string;
}

// ContextBar is a /context-like fill gauge for the context window, segmented by how
// the prompt tokens break down (cache read / cache creation / fresh input). Shared
// by the chat (MirrorView) and terminal (TerminalView) heads so the current context
// fill is always visible, whichever view a claude pane is showing.
export default function ContextBar({ read, create, fresh, model }: ContextBarProps) {
  const used = read + create + fresh;
  const window = contextWindow(model, used);
  const pct = Math.min(100, (used / window) * 100);
  const w = (n: number) => (n / window) * 100 + "%";
  // As the window fills, claude auto-compacts near the top — surface that the strip
  // is approaching that zone instead of a full bar looking the same as an empty one.
  const level = pct >= 93 ? "full" : pct >= 80 ? "near" : "";
  const title =
    `文脈 ${used.toLocaleString()} / ${window.toLocaleString()} トークン (${pct.toFixed(0)}%)\n` +
    `キャッシュ再利用 ${read.toLocaleString()} · 新規キャッシュ ${create.toLocaleString()} · 未キャッシュ ${fresh.toLocaleString()}` +
    (level ? "\nまもなく自動圧縮される可能性があります" : "");
  return (
    <div className={"mirror-ctxbar" + (level ? " cb-" + level : "")} title={title}>
      <div className="cb-track">
        <div className="cb-seg cb-read" style={{ width: w(read) }} />
        <div className="cb-seg cb-create" style={{ width: w(create) }} />
        <div className="cb-seg cb-fresh" style={{ width: w(fresh) }} />
      </div>
      <span className="cb-label">
        {level && <Icon name="warning" />} コンテキスト {fmtTok(used)} / {fmtTok(window)}・{pct.toFixed(0)}%
      </span>
    </div>
  );
}
