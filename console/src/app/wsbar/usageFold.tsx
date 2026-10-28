// --- usage-chip folding ---------------------------------------------------------------
// Five agents expose subscription limits and five chips is most of the bar's right half,
// so the bar keeps the agents actually in use and folds the rest behind one "+N" chip.
// WHERE each chip goes is decided by the group (UsageChipFold) from the ranking in
// app/usageChipPlan.ts; WHETHER a chip has anything to show is only known inside the chip,
// which is why each one reports itself up here instead of the group asking.
//
// A folded chip is not unmounted — it stays exactly where it was in the tree, keeping its
// reading and its 5-minute poll, and renders through a portal into the popover's body. So
// "+N" is a move, not a second fetch: unmounting it would cost an extra request per open
// and, worse, the group could not know a hidden chip had gone near its cap.
interface ChipReport {
  visible: boolean;
  urgent: boolean;
  label: string; // what the chip reads; its width is part of what squeezing saves
}
interface FoldCtxValue {
  report(kind: string, state: ChipReport): void;
  slot(kind: string): "bar" | "fold";
  openFold(): void;
  host: HTMLElement | null; // the popover body, non-null only while it is open
}
const FoldCtx = createContext<FoldCtxValue | null>(null);

// useChipSlot: where this chip draws itself. MUST be called before a chip's early return —
// a chip with nothing to show still has to report that, or the group would count it.
// Outside a group (the phone's ⋯ overflow, which is already a vertical list with room)
// there is no context and every chip stays put.
//
// `reveal` is for the keyboard commands (Ctrl/⌘+K g c / g x / g a): a folded chip renders
// nothing until the popover holding it is open, so toggling it from the keyboard has to
// open that popover too — otherwise the shortcut silently does nothing for exactly the
// agents the user reaches for least often, which is where a shortcut is most useful.
//
// `open` / `close` are the chip's own popover. A chip that stops being drawn — moved into a
// closed +N, or its +N closed — keeps its state, so its popover would stay "open" with a
// dismiss layer that swallows the next click anywhere. Closed on that transition only: a
// keyboard reveal opens a folded chip one render before the +N body exists to draw it.
function useChipSlot(kind: string, visible: boolean, urgent: boolean, label: string, open: boolean, close: () => void) {
  const ctx = useContext(FoldCtx);
  const report = ctx?.report;
  useEffect(() => {
    report?.(kind, { visible, urgent, label });
  }, [report, kind, visible, urgent, label]);
  const slot = ctx ? ctx.slot(kind) : ("bar" as const);
  const drawn = slot === "bar" || !!ctx?.host;
  const wasDrawn = useRef(drawn);
  useEffect(() => {
    if (wasDrawn.current && !drawn && open) close();
    wasDrawn.current = drawn;
  }, [drawn, open, close]);
  return {
    slot,
    host: ctx?.host || null,
    reveal: () => slot === "fold" && ctx?.openFold(),
  };
}

// placeChip: render the chip where the group put it. The folded copy carries the agent's
// name (the popover is a list, not a row of glyphs) — always in the markup, shown by CSS
// only inside the popover, so moving a chip never re-renders different children.
function placeChip(slot: "bar" | "fold", host: HTMLElement | null, chip: ReactElement) {
  if (slot !== "fold") return chip;
  return host ? createPortal(chip, host) : null;
}

const PLACE_MODES = [
  { mode: "pin", key: "wsbar.usage.place_pin", titleKey: "wsbar.usage.place_pin_title" },
  { mode: "auto", key: "wsbar.usage.place_auto", titleKey: "wsbar.usage.place_auto_title" },
  { mode: "fold", key: "wsbar.usage.place_fold", titleKey: "wsbar.usage.place_fold_title" },
] as const satisfies readonly { mode: string; key: MsgKey; titleKey: MsgKey }[];

// UsageChipPlace: the user's override of the ranking, in the chip's own dropdown — that is
// the surface you are looking at the moment you think "I never use this one". Pinning is
// also how you ask for a third chip on the bar, which is why there is no count setting.
function UsageChipPlace({ kind }: { kind: string }) {
  const tr = useT();
  const s = useSettings();
  const pinned = (s.usageChipsPinned || []).includes(kind);
  const folded = (s.usageChipsFolded || []).includes(kind);
  const mode = pinned ? "pin" : folded ? "fold" : "auto";
  const without = (list: string[]) => (list || []).filter((k) => k !== kind);
  const pick = (m: "pin" | "auto" | "fold") =>
    setSettings({
      usageChipsPinned: m === "pin" ? [...without(s.usageChipsPinned), kind] : without(s.usageChipsPinned),
      usageChipsFolded: m === "fold" ? [...without(s.usageChipsFolded), kind] : without(s.usageChipsFolded),
    });
  return (
    <div className="wu-place">
      <span className="wu-label">{tr("wsbar.usage.place_label")}</span>
      <div className="wu-place-seg">
        {PLACE_MODES.map((m) => (
          <Button
            key={m.mode}
            variant="ghost"
            className={mode === m.mode ? "on" : undefined}
            aria-pressed={mode === m.mode}
            title={tr(m.titleKey)}
            onClick={() => pick(m.mode)}
          >
            {tr(m.key)}
          </Button>
        ))}
      </div>
    </div>
  );
}

// The chips in bar order. Every agent that can ever show a limit chip is listed, whether or
// not it has a reading — the ranking needs the full order to break ties, and each chip
// reports its own visibility anyway.
const USAGE_CHIP_ORDER: string[] = [...USAGE_SOURCES.map((s) => s.kind as string), "copilot", "agy"];

// UsageChipFold: the group that decides which chips keep a slot on the bar. It owns the
// "+N" chip, the popover the folded chips portal into, and the ranking; the chips
// themselves only report what they are (see useChipSlot).
// `squeeze`: the bar is out of width (app/wsBarFold.ts), so no chip keeps an auto-ranked slot;
// pinned and near-cap chips still do, since those are the user's word and a warning.
// `onLayoutChange` hears which chips sit on the bar with and without the squeeze, so the bar
// knows when what squeezing saves has changed.
export function UsageChipFold({
  children,
  squeeze = false,
  onLayoutChange,
}: {
  children: ReactElement;
  squeeze?: boolean;
  onLayoutChange?: (key: string) => void;
}) {
  const tr = useT();
  const [reports, setReports] = useState<Record<string, ChipReport>>({});
  const [open, setOpen] = useState(false);
  const [host, setHost] = useState<HTMLDivElement | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const settings = useSettings();

  // Stable identity: a report() that changed every render would re-fire every chip's
  // effect, and the returned state keeps the same object when nothing moved so a steady
  // bar does not re-render on each poll.
  const report = useCallback((kind: string, state: ChipReport) => {
    setReports((cur) => {
      const prev = cur[kind];
      if (prev && prev.visible === state.visible && prev.urgent === state.urgent && prev.label === state.label) return cur;
      return { ...cur, [kind]: state };
    });
  }, []);

  // Rank by recent use, off the session list the Console already polls. The selector
  // returns a string so this component only re-renders when the ranking's input changed,
  // not on every published list.
  const useKey = useSessionsStore((s) =>
    s.sessions.map((x) => `${x.kind}:${x.alive ? 1 : 0}:${x.createdAt || ""}`).join("|"),
  );
  const [stamps, setStamps] = useState<KindStamps>(readKindStamps);
  useEffect(() => {
    setStamps(noteSessionKinds(useSessionsStore.getState().sessions));
  }, [useKey]);

  const planInput = {
    order: USAGE_CHIP_ORDER,
    visible: USAGE_CHIP_ORDER.filter((k) => reports[k]?.visible),
    urgent: USAGE_CHIP_ORDER.filter((k) => reports[k]?.urgent),
    pinned: settings.usageChipsPinned,
    folded: settings.usageChipsFolded,
    stamps,
  };
  const roomy = planUsageChips({ ...planInput, inline: AUTO_INLINE });
  const tight = planUsageChips({ ...planInput, inline: 0 });
  const plan = squeeze ? tight : roomy;
  // Both layouts, whichever is drawn: what squeezing saves is the difference between them,
  // and a pin or a near-cap chip can change the squeezed one alone. So can a reading: the
  // chips that squeezing hides sit in a closed +N, drawn nowhere, so a label that shortens
  // there ("94% / 94%" → "0% / 0%") reaches the bar only through this key. Not keyed on
  // `squeeze`, or the bar's own measuring unfold/refold would look like a change.
  const tightSet = new Set(tight.bar);
  const saved = roomy.bar.filter((k) => !tightSet.has(k));
  const layoutKey =
    roomy.bar.join(",") + "|" + tight.bar.join(",") + "|" + saved.map((k) => reports[k]?.label ?? "").join(",");
  useEffect(() => {
    onLayoutChange?.(layoutKey);
  }, [onLayoutChange, layoutKey]);
  // The +N vanishes when nothing is left to fold (the bar widened, a pin): its popover goes
  // with it, so its state and dismiss layer must too, or the next click anywhere is eaten.
  const hasFold = plan.fold.length > 0;
  useDismiss(ref, open && hasFold, () => setOpen(false));
  useEffect(() => {
    if (!hasFold) setOpen(false);
  }, [hasFold]);
  const foldSet = new Set(plan.fold);
  const value: FoldCtxValue = {
    report,
    slot: (kind) => (foldSet.has(kind) ? "fold" : "bar"),
    openFold: () => setOpen(true),
    host,
  };

  return (
    <FoldCtx.Provider value={value}>
      {children}
      {plan.fold.length > 0 && (
        <div className="ws-usage-wrap ws-fold" ref={ref}>
          <Button
            variant="ghost"
            className="ws-fold-btn"
            title={tr("wsbar.usage.fold_title", {
              names: plan.fold.map((k) => kindLabel(k as SessionKind)).join(tr("common.list_sep")),
            })}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
          >
            +{plan.fold.length}
            <Icon name="chevron-down" />
          </Button>
          {open && (
            <div className="ws-fold-pop">
              <div className="wu-title">{tr("wsbar.usage.fold_pop_title")}</div>
              {/* The folded chips portal in here. setHost is a state setter, so its identity
                  is stable — a fresh ref callback each render would detach and re-attach the
                  node on every one of them. */}
              <div className="ws-fold-list" ref={setHost} />
              <div className="wu-note muted">{tr("wsbar.usage.fold_hint")}</div>
            </div>
          )}
        </div>
      )}
    </FoldCtx.Provider>
  );
}
