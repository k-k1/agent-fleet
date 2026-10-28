import { useCallback, useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { kindClass, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { tCount, useT } from "../../lib/i18n/index.ts";
import { useUsageResetNotify } from "../usageResetNotify.ts";
import { pickNearCap } from "../usageNearCap.ts";
import { agoText, expiryText, resetChipText, untilText, UsageBreakdownLink, UsageRow, type UsageWindow, useUsage, whenText } from "./usageCommon.tsx";
import { placeChip, UsageChipPlace, useChipSlot } from "./usageFold.tsx";
import { type UsageSource } from "./usageSources.ts";

// UsageChip: a compact per-agent limit chip (glyph + the two window percentages) that
// opens a dropdown with each window's bar + reset + a reload. Renders null until the
// agent's endpoint answers with data, so it self-hides for agents the user doesn't use.
export function UsageChip({ src, tenant }: { src: UsageSource; tenant: string | null }) {
  const tr = useT();
  const fiveLabel = tr(src.fiveLabelKey);
  const weekLabel = tr(src.weekLabelKey);
  const { usage, refreshing, refresh } = useUsage(tenant, src.endpoint);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  const closePop = useCallback(() => setOpen(false), []);
  // Keyboard: Ctrl/⌘+K g c / g x toggles this agent's usage popover. `reveal` (declared
  // below, once the chip knows where it sits) opens the fold popover first when this chip
  // lives inside it — the callback runs from an effect, so reading it here is safe.
  useOpenSignal(src.key, () => {
    setOpen((o) => !o);
    reveal();
  });
  // Notify when a constrained limit window resets (5-hour / weekly). Runs whether or
  // not the dropdown is open — the chip stays mounted while the workspace is up.
  useUsageResetNotify(src, usage, refresh);

  const win = (w: UsageWindow) => ({
    pct: Math.round(w.pct),
    until: untilText(w.resetsAt),
    when: whenText(w.resetsAt),
    stale: !!w.stale,
  });
  const uh = usage && usage.fiveHour && win(usage.fiveHour);
  const uw = usage && usage.sevenDay && win(usage.sevenDay);
  const resetCount = usage?.resetCredits?.availableCount || 0;
  const fullResets = (usage?.resetCredits?.credits || [])
    .filter((r): r is { expiresAt: string } => !!r?.expiresAt && !isNaN(new Date(r.expiresAt).getTime()))
    .sort((a, b) => new Date(a.expiresAt).getTime() - new Date(b.expiresAt).getTime());
  const unavailable = !uh && !uw && !resetCount;

  // Near the cap, the chip shows when the block lifts instead of the %: the latest reset among
  // the live capped windows (see pickNearCap). The window, its % and the relative form go in
  // the tooltip.
  const bind = pickNearCap([
    ...(usage?.fiveHour ? [{ label: fiveLabel, ...usage.fiveHour }] : []),
    ...(usage?.sevenDay ? [{ label: weekLabel, ...usage.sevenDay }] : []),
  ]);

  // Computed before the early return below, because useChipSlot is a hook: a chip with
  // nothing to show has to say so rather than skip its turn. `bind` moved above the return
  // for the same reason — the group promotes a near-cap chip onto the bar.
  const visible = !(unavailable && !usage?.authed);
  const label = unavailable
    ? "—"
    : bind
      ? resetChipText(bind.resetsAt)
      : [uh && (uh.stale ? "—" : `${uh.pct}%`), uw && (uw.stale ? "—" : `${uw.pct}%`)].filter(Boolean).join(" / ");
  const { slot, host, reveal } = useChipSlot(src.kind, visible, !!bind || !!resetCount, label, open, closePop);
  // Hide the chip only when the user isn't signed into this agent (nothing to show, and
  // never will be). If they ARE signed in but the (unofficial, rate-limited) reading is
  // momentarily unavailable, keep a degraded chip so it never vanishes on a transient
  // failure — its dropdown links out to the vendor's own usage page to check manually.
  if (!visible) return null;

  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: kindLabel(src.kind) })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: kindLabel(src.kind),
          label: bind.label,
          pct: Math.round(bind.pct),
          until: untilText(bind.resetsAt),
          when: whenText(bind.resetsAt),
        })
      : tr("wsbar.usage.title", { name: kindLabel(src.kind) });

  return placeChip(
    slot,
    host,
    <div className="ws-usage-wrap" ref={ref}>
      {/* Same badge look as the Sessions-list kind badge: reuse kind-tag + the kind
          color, then reset the button chrome (ws-usage-btn). */}
      <Button
        className={"kind-tag kind-" + kindClass(src.kind) + " ws-usage-btn"}
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={kindIcon(src.kind)} />
        <span className="ws-usage-name">{kindLabel(src.kind)}</span>
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        {!!resetCount && (
          <span className={"ws-reset-count" + (fullResets.length && new Date(fullResets[0].expiresAt).getTime() - Date.now() <= 7 * 86400000 ? " warn" : "")}
            title={tCount("wsbar.usage.full_reset", resetCount) + (fullResets.length ? tr("wsbar.usage.full_reset_soonest", { when: whenText(fullResets[0].expiresAt) }) : "")}>
            +{resetCount}
          </span>
        )}
        <Icon name="chevron-down" />
      </Button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: kindLabel(src.kind) })}</div>
          {/* Account + subscription tier: claude's HandleUsage returns `user`/`plan`,
              codex returns `user`/`planType` — surface whichever is present. */}
          {usage?.user && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.user })}</div>}
          {(usage?.planType || usage?.plan) && (
            <div className="wu-note muted">{tr("wsbar.usage.plan", { plan: usage?.planType || usage?.plan || "" })}</div>
          )}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: kindLabel(src.kind) })}</div>
          ) : (
            <>
              {uh && <UsageRow label={fiveLabel} w={uh} />}
              {uw && <UsageRow label={weekLabel} w={uw} />}
              {!!resetCount && (
                <div className="wu-full-resets">
                  <div className="wu-row-head"><span className="wu-label">{tr("wsbar.usage.full_reset_label")}</span><span>{tr("wsbar.usage.count_ken", { count: resetCount })}</span></div>
                  {fullResets.map((reset, i) => (
                    <div className="wu-expiry" key={`${reset.expiresAt}-${i}`}>
                      <span>{tr("wsbar.usage.expires", { date: whenText(reset.expiresAt).split(" ")[0] })}</span>
                      <span className={new Date(reset.expiresAt).getTime() - Date.now() <= 7 * 86400000 ? "warn" : "muted"}>{expiryText(reset.expiresAt)}</span>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            {src.live && (
              <Button variant="ghost" className="wu-reload" onClick={refresh} disabled={refreshing}>
                <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
              </Button>
            )}
          </div>
          {!unavailable && src.noteKey && <div className="wu-note muted">{tr(src.noteKey)}</div>}
          <UsageChipPlace kind={src.kind} />
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
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
