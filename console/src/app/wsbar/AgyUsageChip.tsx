// AgyUsageChip: Antigravity's quota chip (docs/log/32 — moved here from the AgyCard so
// the remaining balance sits beside the Claude / Codex chips). agy's wallet is split into
// per-model-group pools (Gemini / Claude+GPT), each with a weekly window plus a
// 5-hour window on paid tiers, and the agent reports REMAINING percent (matching
// the TUI's own /usage bars). Shown as USED percent so the chip and dropdown read
// exactly like the other agents'. The chip numbers are the first group's (Gemini —
// agy's default-model pool); the dropdown lists every group's windows. Self-hides
// while agy isn't signed in (authed:false), same rule as the generic chip.
export function AgyUsageChip({ tenant }: { tenant: string | null }) {
  const tr = useT();
  const { usage, refreshing, refresh } = useUsage(tenant, "api/connections/agy/usage");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  const closePop = useCallback(() => setOpen(false), []);
  // Keyboard: Ctrl/⌘+K g a toggles this popover (g c / g x = Claude / Codex). See the
  // generic chip for why `reveal` is read before its declaration.
  useOpenSignal("usage-agy", () => {
    setOpen((o) => !o);
    reveal();
  });

  // Flatten groups into rows: per group the weekly window, then the 5h one.
  const used = usedFromRemaining;
  const wins: { label: string; pct: number; resetsAt?: string; stale?: boolean }[] = [];
  for (const g of (usage?.ok && Array.isArray(usage.groups) && usage.groups) || []) {
    wins.push({ label: tr("wsbar.usage.agy.week_row", { group: g.label }), pct: used(g.remainingPct), resetsAt: g.resetsAt, stale: !!g.stale });
    if (g.fiveHour)
      wins.push({
        label: tr("wsbar.usage.agy.five_row", { group: g.label }),
        pct: used(g.fiveHour.remainingPct),
        resetsAt: g.fiveHour.resetsAt,
        stale: !!g.fiveHour.stale,
      });
  }
  const unavailable = wins.length === 0;

  // Near the cap: the latest live capped reset (same rule as UsageChip).
  const bind = pickNearCap(wins);

  // Report before the early return (see useChipSlot), same as the generic chip.
  const visible = !(unavailable && !usage?.authed);
  // Chip numbers: the first group (Gemini), 5h% / weekly% — same order as the other chips.
  const g0 = usage?.ok && Array.isArray(usage.groups) ? usage.groups[0] : null;
  const label = unavailable
    ? "—"
    : bind
      ? resetChipText(bind.resetsAt!)
      : [
          g0?.fiveHour && (g0.fiveHour.stale ? "—" : `${used(g0.fiveHour.remainingPct)}%`),
          g0 && (g0.stale ? "—" : `${used(g0.remainingPct)}%`),
        ]
          .filter(Boolean)
          .join(" / ");
  const { slot, host, reveal } = useChipSlot("agy", visible, !!bind, label, open, closePop);
  if (!visible) return null;

  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: "Antigravity" })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: "Antigravity",
          label: bind.label,
          pct: bind.pct,
          until: untilText(bind.resetsAt!),
          when: whenText(bind.resetsAt!),
        })
      : tr("wsbar.usage.title", { name: "Antigravity" });

  return placeChip(
    slot,
    host,
    <div className="ws-usage-wrap" ref={ref}>
      <Button
        className={"kind-tag kind-" + kindClass("agy") + " ws-usage-btn"}
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={kindIcon("agy")} />
        <span className="ws-usage-name">{kindLabel("agy")}</span>
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        <Icon name="chevron-down" />
      </Button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: "Antigravity" })}</div>
          {usage?.account && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.account })}</div>}
          {usage?.plan && <div className="wu-note muted">{tr("wsbar.usage.plan", { plan: usage.plan })}</div>}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: "Antigravity" })}</div>
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
          {/* Experimental-tier note (docs/log/32 Track C-3): the Starter pool is tiny and
              shared with the Antigravity IDE / Jules, so the popover always says so. */}
          <div className="wu-note muted">{tr("agents.agy_exp_label")}</div>
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            <Button variant="ghost" className="wu-reload" onClick={refresh} disabled={refreshing}>
              <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
            </Button>
          </div>
          <UsageChipPlace kind="agy" />
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
        </div>
      )}
    </div>,
  );
}
