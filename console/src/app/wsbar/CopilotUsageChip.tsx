import { useCallback, useRef, useState } from "react";
import { usedFromRemaining } from "../../features/sessions/move.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { kindClass, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { pickNearCap } from "../usageNearCap.ts";
import { agoText, resetChipText, untilText, UsageBreakdownLink, UsageRow, useUsage, whenText } from "./usageCommon.tsx";
import { placeChip, UsageChipPlace, useChipSlot } from "./usageFold.tsx";

// CopilotUsageChip: GitHub Copilot's account credit chip. Unlike agy this needs no
// TUI scrape — the backend reads copilot_internal/user (gh transparent auth) and
// returns structured {plan, sku, resetsAt, quotas:[{id, remainingPct, ...}]}. Each
// quota pool (chat / completions / premium_interactions, plan-dependent) shares the
// one monthly reset date. The plan is shown in the popover (the user asked to see it).
export function CopilotUsageChip({ tenant }: { tenant: string | null }) {
  const tr = useT();
  const { usage, refreshing, refresh } = useUsage(tenant, "api/copilot/usage");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  const closePop = useCallback(() => setOpen(false), []);
  useOpenSignal("usage-copilot", () => {
    setOpen((o) => !o);
    reveal();
  });

  const used = usedFromRemaining;
  // Localized pool names; unknown ids fall back to the raw id.
  const poolLabel = (id: string) =>
    id === "chat"
      ? tr("wsbar.usage.copilot.pool_chat")
      : id === "completions"
        ? tr("wsbar.usage.copilot.pool_completions")
        : id === "premium_interactions"
          ? tr("wsbar.usage.copilot.pool_premium")
          : id;
  const quotas: { id: string; remainingPct: number }[] =
    (usage?.ok && Array.isArray(usage.quotas) && usage.quotas) || [];
  const resetsAt: string | undefined = usage?.resetsAt;
  const wins = quotas.map((q) => ({ label: poolLabel(q.id), pct: used(q.remainingPct), resetsAt }));
  const unavailable = wins.length === 0;

  const bind = pickNearCap(wins);

  // Report before the early return (see useChipSlot), same as the generic chip.
  const visible = !(unavailable && !usage?.authed);
  // Chip number: the primary pool's used% (quotas are pre-ordered by the backend —
  // premium first on paid plans, else chat).
  const label = unavailable ? "—" : bind ? resetChipText(bind.resetsAt!) : `${wins[0].pct}%`;
  const { slot, host, reveal } = useChipSlot("copilot", visible, !!bind, label, open, closePop);
  if (!visible) return null;

  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: "GitHub Copilot" })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: "GitHub Copilot",
          label: bind.label,
          pct: bind.pct,
          until: untilText(bind.resetsAt!),
          when: whenText(bind.resetsAt!),
        })
      : tr("wsbar.usage.copilot.title");
  const plan: string = usage?.plan || "";

  return placeChip(
    slot,
    host,
    <div className="ws-usage-wrap" ref={ref}>
      <Button
        className={"kind-tag kind-" + kindClass("copilot") + " ws-usage-btn"}
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={kindIcon("copilot")} />
        <span className="ws-usage-name">{kindLabel("copilot")}</span>
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        <Icon name="chevron-down" />
      </Button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: "GitHub Copilot" })}</div>
          {usage?.user && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.user })}</div>}
          {plan && (
            <div className="wu-note muted">
              {tr("wsbar.usage.plan", { plan })}
              {usage?.canUpgrade ? " · " + tr("wsbar.usage.copilot.upgradable") : ""}
            </div>
          )}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: "GitHub Copilot" })}</div>
          ) : (
            wins.map((w) => (
              <UsageRow
                key={w.label}
                label={w.label}
                w={{ pct: w.pct, until: w.resetsAt ? untilText(w.resetsAt) : "", when: w.resetsAt ? whenText(w.resetsAt) : "" }}
              />
            ))
          )}
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            <Button variant="ghost" className="wu-reload" onClick={refresh} disabled={refreshing}>
              <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
            </Button>
          </div>
          <UsageChipPlace kind="copilot" />
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
          {/* Manage link on its own row (same as the generic UsageChip) — cramming it
              into wu-foot alongside the fetched-at line and refresh broke the layout. */}
          <a className="wu-manage" href="https://github.com/settings/copilot" target="_blank" rel="noopener">
            <Icon name="link-external" /> {tr("wsbar.usage.open_page")}
          </a>
        </div>
      )}
    </div>,
  );
}
