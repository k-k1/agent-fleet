// PaneSessionChip — "which session is this pane showing", as drawn in a view
// head: the agent-kind badge, the session's display name, and its status pill.
//
// The terminal and the mirror are two renderings of the SAME session, so their
// heads showed the same chip — and had grown two copies of this markup that
// drifted (raw codicon span vs <Icon>, a dead `kt-label` class, two different
// name classes). One component now, with the state pill passed in: the views
// source it differently on purpose — the mirror prefers its live polled status
// over the session meta, the terminal has only the meta.
import type { Session } from "../../types/session.ts";
import type { StateInfo } from "../../agents/registry.ts";
import { kindIcon, kindLabel, kindShort, kindClass } from "../../lib/sessionkind.ts";
import { displayName } from "../../lib/sessionview.ts";
import { Icon } from "../../ui/Icon.tsx";

interface PaneSessionChipProps {
  session: Session;
  /** Already-derived status pill; null renders the chip without one. */
  state: StateInfo | null;
}

export function PaneSessionChip({ session, state }: PaneSessionChipProps) {
  return (
    // The name ellipsizes in a narrow pane and the slug stays hidden (it is an
    // internal id) — the hover tip carries both in full.
    <span className="pane-session" title={displayName(session) + "\nID: " + session.name}>
      <span className={"kind-tag kind-" + kindClass(session.kind)}>
        <Icon name={kindIcon(session.kind)} />
        <span className="kt-full">{kindLabel(session.kind)}</span>
        <span className="kt-short">{kindShort(session.kind)}</span>
      </span>
      <span className="pane-session-name">{displayName(session)}</span>
      {state && (
        <span className={"session-state " + state.cls}>
          <Icon name={state.icon} spin={state.spin} /> {state.text}
        </span>
      )}
    </span>
  );
}
