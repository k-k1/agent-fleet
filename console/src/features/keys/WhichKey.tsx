// which-key overlay — a passive hint that appears a moment after the leader key,
// listing the next keys (groups then actions) available on the current leader path.
// Reads the registry, so it can never drift from what the dispatcher will actually do.
import { Kbd } from "../../ui/Kbd.tsx";
import { leaderChildren } from "../../lib/keys/registry.ts";
import { useKeysStore } from "./store.ts";
import { GROUPS } from "./commands.ts";
import { useEffectiveCommands, boundChord, APP_LEADER } from "./bindings.ts";
import { cmdLabel } from "./labels.ts";
import { useLocale } from "../../lib/i18n/index.ts";
import { buildContext } from "./dispatcher.ts";

export function WhichKey() {
  const open = useKeysStore((s) => s.whichKeyOpen);
  const path = useKeysStore((s) => s.leaderPath);
  const commands = useEffectiveCommands();
  useLocale(); // re-render on language change
  if (!open) return null;
  const children = leaderChildren(commands, GROUPS, path, buildContext());
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
          <span className="wk-hint">次のキー</span>
        </div>
        <div className="wk-grid">
          {children.map((c) => (
            <div className={"wk-item" + (c.isGroup ? " wk-item-group" : "")} key={c.key}>
              <Kbd chord={c.key} />
              <span className="wk-label">
                {cmdLabel(c.title)}
                {c.isGroup && <span className="wk-more"> …</span>}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
