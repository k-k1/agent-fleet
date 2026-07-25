import type { ReactNode } from "react";
import { fmtTok } from "../../lib/fmttok.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { fmtNum } from "../../lib/intl.ts";

// contextWindow returns the model's context length. The current Claude family
// (Opus 5/4.8/4.7/4.6, Sonnet 5/4.6, Fable/Mythos 5) is 1M-native — 1M is the default
// window, not a 200k default you grow into. GPT-5.x is 272k (codex normally records
// its real window; this is the fallback for paths without it, e.g. the assistant
// chat's codex exec). Haiku is 200k. Unknown/older models assume 200k but grow to
// fit if a 1M beta is clearly in use.
// Mirrored in Go as contextWindowGuess() (workspace/agent/session_usage.go, the MCP
// get_session_usage aggregation) — keep the two in sync.
export function contextWindow(model: string | null | undefined, used: number): number {
  const m = (model || "").toLowerCase();
  if (/opus-(4-[678]|5)|sonnet-(4-6|5)|fable-5|mythos-5/.test(m)) return 1000000;
  if (/gpt-5/.test(m)) return 272000;
  if (/haiku/.test(m)) return 200000;
  return used > 200000 ? 1000000 : 200000;
}

interface ContextBarProps {
  read: number;
  create: number;
  fresh: number;
  model?: string;
  // Explicit context-window size when the agent records it (codex model_context_window);
  // overrides the model-name guess so the gauge % is exact.
  window?: number;
  // Optional per-turn spend series (chat only): renders a token-spend trend Sparkline on
  // the same row, after the context gauge. Omitted by the terminal head, which has none.
  spends?: number[];
  maxSpend?: number;
  // Optional trailing action (assistant chat only: the compact button, docs/33) rendered
  // at the row's end. The mirror/terminal heads pass nothing.
  action?: ReactNode;
}

// ContextBar is a /context-like fill gauge for the context window, segmented by how
// the prompt tokens break down (cache read / cache creation / fresh input). Shared
// by the chat (MirrorView) and terminal (TerminalView) heads so the current context
// fill is always visible, whichever view a claude pane is showing.
export function ContextBar({ read, create, fresh, model, window: windowOverride, spends, maxSpend, action }: ContextBarProps) {
  const tr = useT();
  const used = read + create + fresh;
  // Prefer the agent-reported window (exact); fall back to the model-name guess.
  const window = windowOverride && windowOverride > 0 ? windowOverride : contextWindow(model, used);
  const pct = Math.min(100, (used / window) * 100);
  const w = (n: number) => (n / window) * 100 + "%";
  // As the window fills, claude auto-compacts near the top — surface that the strip
  // is approaching that zone instead of a full bar looking the same as an empty one.
  const level = pct >= 93 ? "full" : pct >= 80 ? "near" : "";
  const title =
    tr("mirror.ctx_title", { used: fmtNum(used), window: fmtNum(window), pct: pct.toFixed(0) }) +
    "\n" +
    tr("mirror.ctx_breakdown", { read: fmtNum(read), create: fmtNum(create), fresh: fmtNum(fresh) }) +
    (level ? "\n" + tr("mirror.ctx_near_compact") : "");
  return (
    <div className={"mirror-ctxbar" + (level ? " cb-" + level : "")} title={title}>
      <div className="cb-ctx">
        <span className="cb-ctx-label">
          <span className="cb-lbl-full">{tr("mirror.ctx_label")}</span>
          <span className="cb-lbl-short">ctx</span>
        </span>
        <div className="cb-track">
          <div className="cb-seg cb-read" style={{ width: w(read) }} />
          <div className="cb-seg cb-create" style={{ width: w(create) }} />
          <div className="cb-seg cb-fresh" style={{ width: w(fresh) }} />
        </div>
        <span className="cb-label">
          {level && <Icon name="warning" />} {fmtTok(used)} / {fmtTok(window)}
          {tr("common.mid_dot")}
          {pct.toFixed(0)}%
        </span>
      </div>
      {spends && spends.length >= 2 && (
        <>
          <span className="cb-div" aria-hidden="true" />
          <span className="cb-trend" title={tr("mirror.ctx_trend_title")}>
            <span className="cb-trend-label">
              <span className="cb-lbl-full">{tr("mirror.ctx_trend_label")}</span>
              <span className="cb-lbl-short">token</span>
            </span>
            <Sparkline data={spends} width={120} height={14} />
            <span className="cb-trend-peak">{tr("mirror.ctx_trend_peak", { v: fmtTok(maxSpend ?? 0) })}</span>
          </span>
        </>
      )}
      {action}
    </div>
  );
}
