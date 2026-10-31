import { useCallback, useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { kindClasses, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { pickNearCap } from "../usageNearCap.ts";
import { agoText, resetChipText, untilText, UsageBreakdownLink, UsageRow, useUsage, whenText } from "./usageCommon.tsx";
import { placeChip, UsageChipPlace, useChipSlot } from "./usageFold.tsx";
import { AGY_USAGE, COPILOT_USAGE, type RemainingSource } from "./usageSources.ts";

// RemainingUsageChip: the chip for an agent whose endpoint reports what is left of each quota
// pool (REMAINING_SOURCES: Copilot, Antigravity). Shown as USED percent so the chip and dropdown
// read exactly like the other agents'; the dropdown lists every pool. Self-hides while the
// agent isn't signed in (authed:false), same rule as the generic chip.
export function RemainingUsageChip({ src, tenant }: { src: RemainingSource; tenant: string | null }) {
  const tr = useT();
  const { usage, refreshing, refresh } = useUsage(tenant, src);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  const closePop = useCallback(() => setOpen(false), []);
  // Keyboard: Ctrl/⌘+K g … toggles this popover. See the generic chip for why `reveal` is
  // read before its declaration.
  useOpenSignal(src.key, () => {
    setOpen((o) => !o);
    reveal();
  });

  const wins = usage?.ok ? src.pools(usage, tr) : [];
  const unavailable = wins.length === 0;

  // Near the cap: the latest live capped reset (same rule as UsageChip).
  const bind = pickNearCap(wins);

  // Report before the early return (see useChipSlot), same as the generic chip.
  const visible = !(unavailable && !usage?.authed);
  const label = unavailable ? "—" : bind ? resetChipText(bind.resetsAt!) : src.chipNums(usage!, wins);
  const { slot, host, reveal } = useChipSlot(src.kind, visible, !!bind, label, open, closePop);
  if (!visible) return null;

  const name = src.name;
  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name,
          label: bind.label,
          pct: bind.pct,
          until: untilText(bind.resetsAt!),
          when: whenText(bind.resetsAt!),
        })
      : src.titleKey
        ? tr(src.titleKey)
        : tr("wsbar.usage.title", { name });
  const account = usage?.[src.accountField];
  const plan: string = usage?.plan || "";

  return placeChip(
    slot,
    host,
    <div className="ws-usage-wrap" ref={ref}>
      <Button
        className={"kind-tag " + kindClasses(src.kind) + " ws-usage-btn"}
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={kindIcon(src.kind)} />
        <span className="ws-usage-name">{kindLabel(src.kind)}</span>
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        <Icon name="chevron-down" />
      </Button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name })}</div>
          {account && <div className="wu-note muted">{tr("wsbar.usage.user", { user: account })}</div>}
          {plan && (
            <div className="wu-note muted">
              {tr("wsbar.usage.plan", { plan })}
              {src.upgradableKey && usage?.canUpgrade ? " · " + tr(src.upgradableKey) : ""}
            </div>
          )}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name })}</div>
          ) : (
            wins.map((w) => (
              <UsageRow
                key={w.label}
                label={w.label}
                w={{
                  pct: w.pct,
                  until: w.resetsAt ? untilText(w.resetsAt) : "",
                  when: w.resetsAt ? whenText(w.resetsAt) : "",
                  stale: w.stale,
                }}
              />
            ))
          )}
          {src.noteKey && <div className="wu-note muted">{tr(src.noteKey)}</div>}
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            <Button variant="ghost" className="wu-reload" onClick={refresh} disabled={refreshing}>
              <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
            </Button>
          </div>
          <UsageChipPlace kind={src.kind} />
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
          {/* Manage link on its own row (same as the generic UsageChip) — cramming it into
              wu-foot alongside the fetched-at line and refresh broke the layout. */}
          {src.manageURL && (
            <a className="wu-manage" href={src.manageURL} target="_blank" rel="noopener">
              <Icon name="link-external" /> {tr("wsbar.usage.open_page")}
            </a>
          )}
        </div>
      )}
    </div>,
  );
}

// One agent's chip by name, for a caller that mounts a single agent (WsBarUsageFold.dom.test).
export const CopilotUsageChip = ({ tenant }: { tenant: string | null }) => <RemainingUsageChip src={COPILOT_USAGE} tenant={tenant} />;
export const AgyUsageChip = ({ tenant }: { tenant: string | null }) => <RemainingUsageChip src={AGY_USAGE} tenant={tenant} />;
