// English カタログ / ドメイン: fleetgraph
// キー接頭辞: fgraph
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { fleetgraph as jaFleetgraph } from "../ja/fleetgraph.ts";

export const fleetgraph: Record<keyof typeof jaFleetgraph, string> = {
  "fgraph.count": "{n} lanes",
  "fgraph.show_archived": "Archived",
  "fgraph.show_archived_hint": "Show/hide archived lanes",
  "fgraph.zoom_in": "Zoom in (narrow the window)",
  "fgraph.zoom_out": "Zoom out (widen the window)",
  "fgraph.pan_left": "Pan into the past",
  "fgraph.pan_right": "Pan toward now",
  "fgraph.reset_window": "Reset to the last 24 hours",
  "fgraph.empty": "No lanes",
  "fgraph.load_failed": "Failed to load the fleet graph",

  // The round trip to the sessions overview (ADR 0096 decision 10's "way in"). Two views
  // of one surface, so the default is a swap inside the same pane.
  "fgraph.switch_to_sessions": "List",
  "fgraph.switch_to_sessions_hint": "Switch to the sessions overview (Ctrl/⌘ for a new pane)",

  // Family collapse. The count is how many descendants are hidden, shown on the parent's
  // row so that folding them away is visible.
  "fgraph.collapse": "Collapse child sessions",
  "fgraph.expand": "Expand child sessions",
  "fgraph.hidden_children": "+{n}",
  "fgraph.hidden_children_hint": "{n} child sessions hidden",

  // A deleted lane (ADR 0096 decision 6). The contract hands out only the bare slug, so
  // the "deleted" wording is composed here rather than showing the slug alone.
  "fgraph.erased_label": "Deleted · {id}",

  // Activity-band state words (LedgerState — a different vocabulary from
  // types/session.ts's SessionState).
  "fgraph.state.working": "Working",
  "fgraph.state.compacting": "Compacting (working)",
  "fgraph.state.idle": "Idle",
  "fgraph.state.question": "Has a question",
  "fgraph.state.plan": "Awaiting plan approval",
  "fgraph.state.permission": "Awaiting permission",
  "fgraph.state.blocked": "Awaiting a choice",
  "fgraph.state.auth": "Auth expired",
  "fgraph.state.limited": "Awaiting a usage limit",
  "fgraph.state.spend_limit": "Spend limit",
  "fgraph.state.failed": "Failed",
  "fgraph.state.aborted": "Aborted",
  "fgraph.state.unknown": "Unrecognised state",

  // "Unknown" has two sources (ADR 0096 decision 3, the type's own comment). The wording
  // must stay apart — never say "not observed" over a stretch that WAS observed.
  "fgraph.seg_not_observed": "Nobody observed this stretch",
  "fgraph.seg_unrecognized": "Observed, unrecognised spelling: {raw}",

  // Missing parent (decision 9). No id shown — unlike an erased lane, the contract does
  // not license a bare slug here, and this is often just "off this window" rather than
  // deleted.
  "fgraph.parent_missing": "Parent lane {id} isn't drawn in this figure",

  // Lane line style (ADR 0096 decision 12).
  "fgraph.presence_live": "Live",
  "fgraph.presence_stopped": "Stopped — resumable",
  "fgraph.presence_archived": "Archived — restorable",
  "fgraph.presence_gone": "Gone",
  // The same four words at chip length. Only a lane the live list does not carry reaches
  // these (archived or deleted) — a live one shows stateInfo's own wording instead.
  "fgraph.presence_short_live": "Running",
  "fgraph.presence_short_stopped": "Stopped",
  "fgraph.presence_short_archived": "Archived",
  "fgraph.presence_short_gone": "Gone",

  // The × (a run's end). Not "when it stopped" — "when the end was first observed".
  "fgraph.death_at": "Observed stopped at {time} (may be later than when it actually stopped)",
  "fgraph.death_reason": "Reason: {reason}",
  "fgraph.revive_at": "Resumed at {time}",
  // The newest run ended without a death event (ADR 0096 decision 12, LaneRun.cut).
  "fgraph.run_cut": "Last observed at {time} — unknown after this",

  // The 3-tier retention boundary (CoverageMark).
  "fgraph.mark_activity_start": "No earlier activity bands or arrows are retained",
  "fgraph.mark_lineage_start": "No earlier lineage is retained",
  "fgraph.mark_backfill_start": "Before this, only a birth/death sketch rebuilt from Meta at install",

  // Arrows (ADR 0096 decision 4 / 8-2).
  "fgraph.arrow_spawn": "Spawned",
  "fgraph.arrow_fork": "Forked",
  "fgraph.arrow_handoff": "Handoff",
  "fgraph.arrow_instruct": "Instruction",
  "fgraph.arrow_report": "Report",
  "fgraph.arrow_peer": "Session-to-session message · {intent}",

  // Actors that never become a lane (ADR 0096 decision 8-2).
  "fgraph.actor_conv": "Conversation",
  "fgraph.actor_user": "Person",
  "fgraph.actor_schedule": "Scheduled execution",
  "fgraph.actor_bridge_discord": "Discord bridge",
  "fgraph.actor_bridge_slack": "Slack bridge",
  "fgraph.actor_agent": "Auto-resume",
};
