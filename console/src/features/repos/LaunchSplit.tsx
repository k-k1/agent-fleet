// LaunchSplit — the Launch split button and its per-kind quick menu, shared by the
// Repositories row (RepoRow) and the Active view's repository heading. One component so the
// two places cannot drift in look, size on touch, label or glyphs: the styles are
// .launch-split / .launch-main / .launch-caret / .launch-menu in repos.css.
import type { ComponentPropsWithRef, ReactNode, Ref } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { kindIcon, kindLabel } from "../../lib/sessionkind.ts";

type ButtonExtras = Omit<ComponentPropsWithRef<"button">, "className" | "type" | "children">;

export interface LaunchSplitProps {
  running: boolean;
  /** At least one coding agent can be started in a directory: without one the dialog has
   *  nothing to offer, so the main button stays disabled. */
  hasAgents: boolean;
  /** Replaces the main button's disabled state and tooltip when the caller knows a more
   *  specific reason (a missing checkout). */
  unavailable?: string;
  /** The Active heading names the repository, because several headings share one pane. */
  mainLabel?: string;
  /** The caret may also be disabled by something other than a stopped workspace. */
  caretDisabled?: boolean;
  mainRef?: Ref<HTMLButtonElement>;
  caretRef?: Ref<HTMLButtonElement>;
  splitRef?: Ref<HTMLDivElement>;
  /** Extra attributes for each button (aria-haspopup / aria-expanded). */
  mainProps?: ButtonExtras;
  caretProps?: ButtonExtras;
  onMain: () => void;
  onCaret: () => void;
}

export function LaunchSplit({ running, hasAgents, unavailable, mainLabel, caretDisabled, mainRef, caretRef, splitRef, mainProps, caretProps, onMain, onCaret }: LaunchSplitProps) {
  const tr = useT();
  const mainTitle = unavailable ?? (running ? (hasAgents ? tr("repo.launch_title") : tr("repo.no_agents")) : tr("mirror.ws_stopped"));
  const mainDisabled = !running || !hasAgents || !!unavailable;
  return (
    <div className="launch-split" ref={splitRef}>
      <button
        type="button"
        className="launch-main"
        ref={mainRef}
        title={mainTitle}
        disabled={mainDisabled}
        {...(mainLabel ? { "aria-label": mainLabel } : null)}
        {...mainProps}
        onClick={onMain}
      >
        <Icon name="play" /> {tr("launch.launch")}
      </button>
      <button
        type="button"
        className="launch-caret"
        ref={caretRef}
        title={running ? unavailable ?? tr("repo.quick_launch") : tr("mirror.ws_stopped")}
        disabled={!running || !!caretDisabled}
        {...caretProps}
        onClick={onCaret}
      >
        <Icon name="chevron-down" />
      </button>
    </div>
  );
}

/** The ▼ menu's per-kind items. A plain click launches; Ctrl/⌘-click and middle-click open the
 *  new session in a split pane. `children` follows a separator (the Repositories row adds
 *  its image studio entry there). `menu`: the items carry role=menuitem themselves, for a parent that is
 *  a role=menu: the roving-focus hook stamps roles only on the items present when the menu opens,
 *  so an item added by a later poll would otherwise have none. */
export function LaunchKindItems({ kinds, onPick, menu, children }: { kinds: string[]; onPick: (kind: string, split: boolean) => void; menu?: boolean; children?: ReactNode }) {
  const tr = useT();
  return (
    <>
      {kinds.map((k) => (
        <button
          key={k}
          type="button"
          className="ui-menu-item"
          {...(menu ? { role: "menuitem", tabIndex: -1 } : null)}
          title={tr("repo.launch_new_pane")}
          onClick={(e) => onPick(k, e.ctrlKey || e.metaKey)}
          onMouseDown={(e) => e.button === 1 && e.preventDefault()}
          onAuxClick={(e) => {
            if (e.button === 1) {
              e.preventDefault();
              onPick(k, true);
            }
          }}
        >
          <Icon name={kindIcon(k)} /> {kindLabel(k)}
        </button>
      ))}
      {children && (
        <>
          <div className="ui-menu-sep" role="separator" />
          {children}
        </>
      )}
    </>
  );
}
