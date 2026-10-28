// A usage source = one agent's subscription-limit chip (Claude / Codex). Both endpoints
// return the same {ok, fiveHour, sevenDay, ageSec} shape (codex reads its rate_limits
// straight from the rollout — no network), so one chip component renders either.
interface UsageSource {
  endpoint: string;
  key: OpenTarget; // keyboard-open target id (uiOpen signal)
  // The agent this chip reports for. Its glyph, color class and short name are READ FROM THE
  // REGISTRY (lib/sessionkind), never restated here: this table used to carry its own copies
  // and they went stale the moment the kinds moved to brand marks, leaving the WS bar as the
  // last place still drawing the old codicons.
  kind: SessionKind;
  fiveLabelKey: MsgKey; // 5-hour window label (i18n key)
  weekLabelKey: MsgKey; // weekly window label (i18n key)
  // live = the endpoint queries the current usage (claude), so a refresh button makes
  // sense. When false (codex), the reading is a snapshot from the last turn — no
  // manual refresh; a note explains it instead.
  live: boolean;
  // noteKey = a caveat about what the numbers ARE, shown under the rows. Not tied to
  // `live`: claude's reading is live and still needs one, because the source rounds.
  noteKey?: MsgKey;
  // manageURL = the agent vendor's own usage/limits page (opened in a new tab from the
  // dropdown), so the user can jump to the authoritative source for the exact numbers.
  manageURL?: string;
}

// Exported for WsBarUsageFold.dom.test.tsx: the folding is a property of the chips as a
// GROUP, so the test mounts the real chips inside the real group rather than a stand-in.
export const USAGE_SOURCES: UsageSource[] = [
  {
    endpoint: "api/claude/usage",
    key: "usage-claude",
    kind: "claude",
    fiveLabelKey: "wsbar.usage.claude.five",
    weekLabelKey: "wsbar.usage.claude.week",
    live: true,
    noteKey: "wsbar.usage.claude.note",
    manageURL: "https://claude.ai/new#settings/usage",
  },
  {
    endpoint: "api/codex/usage",
    key: "usage-codex",
    kind: "codex",
    fiveLabelKey: "wsbar.usage.codex.five",
    weekLabelKey: "wsbar.usage.codex.week",
    live: false,
    noteKey: "wsbar.usage.codex.note",
    manageURL: "https://chatgpt.com/#settings/Usage",
  },
  {
    // muse (ADR 0095 decision 10). Its two windows fill the same two rows, but neither the
    // reading nor the first row's NAME is what the others' are: the value is the last
    // observation a running host pushed over the wire, and the window's length is a wire field
    // (measured 300 minutes) rather than a constant — so the row says "current window" and the
    // note says what was measured, instead of the label asserting five hours.
    //
    // live is false for the reason codex's is: there is nothing to refresh TOWARDS. Asking
    // again returns the same observation until a muse turn completes.
    endpoint: "api/muse/usage",
    key: "usage-muse",
    kind: "muse",
    fiveLabelKey: "wsbar.usage.muse.five",
    weekLabelKey: "wsbar.usage.muse.week",
    live: false,
    noteKey: "wsbar.usage.muse.note",
    manageURL: "https://dev.meta.ai/usage",
  },
];
