// SpendChip — the session's estimated spend against its budget (#1054), on the mirror's context
// row. Clicking opens the budget dialog.
//
// The figure is a whole-transcript read on the Agent (GET /sessions/{name}/spend, cached there for
// a few seconds), so it is fetched here rather than riding the 4 s session-list poll, and only
// while the mirror is open. It is an estimate at list price, and says so: "≈" on the number and
// the reason in the tooltip.
import { useEffect, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { sessionSpend } from "../../core/api/client.ts";
import type { SessionSpend } from "../../core/api/client.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { fmtSpend, spendLevel } from "../sessions/spendBudget.ts";
import type { Session } from "../../types/session.ts";

/** How often an open mirror re-reads the spend. The Agent's own budget check does not depend on
 *  this — it runs on the Agent's tick whether anyone is looking or not. */
export const SPEND_POLL_MS = 30_000;

interface SpendChipProps {
  s: Session;
}

export function SpendChip({ s }: SpendChipProps) {
  const tr = useT();
  const [spend, setSpend] = useState<SessionSpend | null>(null);
  const openBudget = useSessionUI((u) => u.openBudget);

  useEffect(() => {
    let live = true;
    const load = () =>
      void sessionSpend(s.name)
        .then((r) => {
          if (live && r && !r.error) setSpend(r);
        })
        .catch(() => {});
    load();
    // A stopped session's spend does not move; one read is enough until it runs again.
    const id = s.alive ? window.setInterval(load, SPEND_POLL_MS) : 0;
    return () => {
      live = false;
      if (id) window.clearInterval(id);
    };
  }, [s.name, s.alive, s.spendCapUsd]);

  if (!spend) return null;
  const cap = spend.spendCapUsd ?? 0;
  const kids = spend.childrenUsd > 0;
  // Nothing measured and nothing set: no chip. Tokens with no price DO show — a budget that
  // cannot see part of the spend has to say so, not read as "≈$0.00".
  if (!spend.priced && !spend.unpriced && cap <= 0 && !kids) return null;
  const level = spendLevel(spend.spendUsd, cap);
  const unpricedOnly = !!spend.unpriced && !spend.priced;
  const partly = !!spend.unpriced && spend.priced;
  const title =
    tr("sess.budget_chip_title") +
    (unpricedOnly ? "\n" + tr("sess.budget_unpriced") : partly ? "\n" + tr("sess.budget_partly_unpriced") : "");
  return (
    <>
      <span className="cb-div" aria-hidden="true" />
      <button
        type="button"
        className={
          "cb-spend" +
          (level === "warn" || level === "over" ? " cb-spend-" + level : "") +
          (spend.unpriced ? " cb-spend-unpriced" : "")
        }
        title={title}
        onClick={() => openBudget(s)}
        data-testid="spend-chip"
      >
        {unpricedOnly ? tr("sess.budget_chip_unpriced") : fmtSpend(spend.spendUsd) + (partly ? "+" : "")}
        {cap > 0 && <> / ${cap.toFixed(2)}</>}
      </button>
      {kids && (
        <span className="cb-spend-kids" title={tr("sess.budget_children_title")}>
          {tr("sess.budget_children", { spend: fmtSpend(spend.childrenUsd) })}
        </span>
      )}
    </>
  );
}
