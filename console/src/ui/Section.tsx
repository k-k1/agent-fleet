// Section — collapsible left-rail section with a title, optional icon + header
// actions, and a body. `id` keys the open/closed state in localStorage (same
// af-section-<id> keys as the old console, so collapse choices carry over).
// `count` shows a muted tally — the only signal of contents while collapsed.
import { useFold } from "../lib/useFold.ts";
import { createContext, useContext, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./Icon.tsx";
import { useT } from "../lib/i18n/index.ts";
import { usePublishedHeight } from "../lib/usePublishedHeight.ts";

interface SectionProps {
  id?: string;
  title?: ReactNode;
  icon?: string;
  count?: number;
  /** Unread-notification dot, shown only while collapsed: like `count`, it is a signal
   *  about contents the fold has hidden. */
  unread?: boolean;
  actions?: ReactNode;
  defaultOpen?: boolean;
  /** Controlled mode: pass `open` (+ `onToggle`) and the section renders that
   * state instead of owning it — for callers that must open programmatically
   * (e.g. the Files section on a reveal). Persistence is the caller's job then. */
  open?: boolean;
  onToggle?: () => void;
  children?: ReactNode;
}

const storeKey = (id: string) => `af-section-${id}`;

// A section that is the whole of a left-pane view has nothing to fold away from: the view is
// already the member's choice of what to look at, and a folded one would be an empty pane.
// Under SoloSection the section is always open and its header is a plain title. The stored
// fold is left alone, so nothing a section's owner does with it (opening on a reveal) breaks.
const SoloContext = createContext(false);

export function SoloSection({ children }: { children: ReactNode }) {
  return <SoloContext.Provider value={true}>{children}</SoloContext.Provider>;
}

export function Section({ id, title, icon, count, unread, actions, defaultOpen = true, open: openProp, onToggle, children }: SectionProps) {
  const tr = useT();
  const [stored, setStored] = useFold(id ? storeKey(id) : undefined);
  const [localOpen, setLocalOpen] = useState(defaultOpen);
  const openState = id ? stored ?? defaultOpen : localOpen;
  const controlled = openProp !== undefined;
  const solo = useContext(SoloContext);
  const open = solo || (controlled ? openProp : openState);
  // Publish the sticky header band's height as --sec-head-h on the section, so
  // body content that also pins (the filter bar) can offset itself to sit right
  // below the header instead of guessing the height (it differs by header actions).
  const headRef = usePublishedHeight<HTMLDivElement>("--sec-head-h");
  const toggle = () => {
    if (controlled) {
      onToggle?.();
      return;
    }
    if (id) setStored(!openState);
    else setLocalOpen(!openState);
  };
  return (
    <section className={"ui-section" + (open ? "" : " collapsed")}>
      <div ref={headRef} className="ui-section-head">
        {solo ? (
          <div className="ui-section-toggle solo">
            {icon && <Icon name={icon} />}
            <span className="ui-section-title">{title}</span>
            {count != null && count > 0 && <span className="ui-section-count">{count}</span>}
          </div>
        ) : (
          <button type="button" className="ui-section-toggle" onClick={toggle} aria-expanded={open}>
            <span className="ui-section-caret">{open ? "▾" : "▸"}</span>
            {icon && <Icon name={icon} />}
            <span className="ui-section-title">{title}</span>
            {count != null && count > 0 && <span className="ui-section-count">{count}</span>}
            {!open && unread && <span className="unread-dot" role="img" aria-label={tr("noti.unread_session")} />}
          </button>
        )}
        <span className="ui-section-actions">{actions}</span>
      </div>
      {/* Only the outermost section of a view is solo; one nested in its body folds as usual. */}
      {open && (
        <div className="ui-section-body">
          <SoloContext.Provider value={false}>{children}</SoloContext.Provider>
        </div>
      )}
    </section>
  );
}
