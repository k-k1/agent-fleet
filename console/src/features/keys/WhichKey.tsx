// which-key overlay — a passive hint that appears a moment after the leader key,
// listing the next keys (groups then actions) available on the current leader path.
// Reads the registry, so it can never drift from what the dispatcher will actually do.
import { Kbd } from "../../ui/Kbd.tsx";
import { leaderChildren, type LeaderChild } from "../../lib/keys/registry.ts";
import { useKeysStore } from "./store.ts";
import { GROUPS } from "./commands.ts";
import { useEffectiveCommands, boundChord, APP_LEADER } from "./bindings.ts";
import { cmdLabel } from "./labels.ts";
import { useLocale, useT } from "../../lib/i18n/index.ts";
import { buildContext } from "./dispatcher.ts";

// Key-scannable order: digits before letters, then case-insensitive. Punctuation
// (e.g. ",") sorts last so it doesn't split the alphabet. Makes finding a key by eye
// predictable instead of following registration order.
const rank = (k: string): string => {
  const c = k[0] ?? "";
  if (/[0-9]/.test(c)) return "0" + k;
  if (/[a-z]/i.test(c)) return "1" + k.toLowerCase();
  return "2" + k;
};
const byKey = (a: LeaderChild, b: LeaderChild) => rank(a.key).localeCompare(rank(b.key));

function Grid({ items }: { items: LeaderChild[] }) {
  return (
    <div className="wk-grid">
      {items.map((c) => (
        <div className={"wk-item" + (c.isGroup ? " wk-item-group" : "")} key={c.key}>
          <Kbd chord={c.key} />
          <span className="wk-label">
            {cmdLabel(c.title)}
            {c.isGroup && <span className="wk-more"> …</span>}
          </span>
        </div>
      ))}
    </div>
  );
}

export function WhichKey() {
  const open = useKeysStore((s) => s.whichKeyOpen);
  const path = useKeysStore((s) => s.leaderPath);
  const commands = useEffectiveCommands();
  const tr = useT();
  useLocale(); // re-render on language change
  if (!open) return null;
  const children = leaderChildren(commands, GROUPS, path, buildContext());
  const groups = children.filter((c) => c.isGroup).sort(byKey);
  const actions = children.filter((c) => !c.isGroup).sort(byKey);
  // Only caption the sections when both kinds are present; a single-kind menu (e.g. a
  // subgroup that's all actions) reads better with no redundant label.
  const split = groups.length > 0 && actions.length > 0;
  return (
    <div className="wk-overlay">
      <div className="wk-panel">
        <div className="wk-head">
          <span className="wk-seq">
            <Kbd chord={boundChord(APP_LEADER) || "mod+k"} />
            {path.map((k, i) => (
              <Kbd key={i} chord={k} />
            ))}
          </span>
          <span className="wk-hint">{tr("ui.next_key")}</span>
        </div>
        {split && <div className="wk-section">{tr("ui.wk_groups")}</div>}
        {groups.length > 0 && <Grid items={groups} />}
        {split && <div className="wk-section">{tr("ui.wk_actions")}</div>}
        {actions.length > 0 && <Grid items={actions} />}
        <div className="wk-foot">
          {path.length > 0 && (
            <span className="wk-nav">
              <Kbd chord="backspace" /> {tr("ui.wk_back")}
            </span>
          )}
          <span className="wk-nav">
            <Kbd chord="escape" /> {tr("ui.wk_cancel")}
          </span>
        </div>
      </div>
    </div>
  );
}
