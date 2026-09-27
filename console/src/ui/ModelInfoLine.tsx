// ModelInfoLine — what the Agent knows about the selected model beyond its name: API list
// price, context window, release date, and whether it is going away (Issue #1021). Drawn under
// every model picker, so a member compares models where they actually choose one.
//
// Nothing is drawn when nothing is known (cursor, muse, lcpp, claude's tier aliases, an id
// models.dev does not list): an empty line beats a guessed number next to a model someone is
// about to pay for.
import { useT } from "../lib/i18n/index.ts";
import { fmtDateTime } from "../lib/intl.ts";
import { useModelInfo } from "../lib/agentModels.ts";
import type { ModelInfo } from "../lib/agentModels.ts";
import { fmtContext, fmtUSD } from "../lib/modelInfoFormat.ts";

// In UTC: codex writes "retires on October 14" for 2026-10-14T19:00:00Z, which is already the
// 15th in Tokyo, and a date that disagrees with the vendor's own sentence reads as a mistake.
const DATE: Intl.DateTimeFormatOptions = { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" };

/** fmtRetireDate writes the vendor's RFC3339 instant as its calendar date, or passes an
 *  unparseable one through: the vendor's own wording is still better than dropping the date. */
function fmtRetireDate(at: string): string {
  return Number.isNaN(Date.parse(at)) ? at : fmtDateTime(at, DATE);
}

/** The short form for a combo row: "$0.10 / $0.50 · 1.05M". "" when there is nothing to say. */
export function modelInfoCompact(info: ModelInfo | null): string {
  if (!info) return "";
  const parts: string[] = [];
  if (info.price) parts.push(`${fmtUSD(info.price.in)} / ${fmtUSD(info.price.out)}`);
  if (info.context) parts.push(fmtContext(info.context));
  return parts.join(" · ");
}

export function ModelInfoLine({ kind, model }: { kind: string; model: string }) {
  const tr = useT();
  const info = useModelInfo(kind, model);
  if (!info) return null;
  const facts: string[] = [];
  if (info.context) facts.push(tr("ui.mi_context", { n: fmtContext(info.context) }));
  if (info.released) facts.push(tr("ui.mi_released", { date: info.released }));
  const r = info.retiring;
  const warn = r
    ? [r.at ? tr("ui.mi_retiring_on", { date: fmtRetireDate(r.at) }) : tr("ui.mi_retiring"),
       r.successor ? tr("ui.mi_successor", { model: r.successor }) : ""].filter(Boolean).join(" · ")
    : info.deprecated
      ? tr("ui.mi_deprecated")
      : "";
  // opencode is billed through its gateway, so the number is the gateway's and says so.
  const priceLabel =
    kind === "opencode" && info.priceFrom ? tr("ui.mi_gateway_price", { gateway: info.priceFrom }) : tr("ui.mi_list_price");
  return (
    <div className="ui-field-hint model-info-line">
      {info.price && (
        <span className="model-info-price" title={tr("ui.mi_list_price_note")}>
          {tr("ui.mi_price_head", { label: priceLabel })}{" "}
          {tr("ui.mi_price_io", { in: fmtUSD(info.price.in), out: fmtUSD(info.price.out) })}
          {info.price.cacheRead ? " · " + tr("ui.mi_price_cache", { v: fmtUSD(info.price.cacheRead) }) : ""}
        </span>
      )}
      {facts.length > 0 && <span className="model-info-facts">{facts.join(" · ")}</span>}
      {warn && (
        <span className="model-info-warn" title={r?.note || undefined}>
          {warn}
        </span>
      )}
    </div>
  );
}
