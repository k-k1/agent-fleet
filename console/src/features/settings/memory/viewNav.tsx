// The view-nav shared by the two memory entries (Agent memory, Memory snapshots), and the hook
// that decides which view is showing.
//
// A view is picked, in this order, by a deep link ("memory:changes" through openSettings), by the
// view last left open in this entry (device-local, like the rail section), then by the entry's
// own default. The views themselves are standalone components, so this nav is the only thing
// that ties them to the Settings modal.

import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useSettingsUI, lastSectionView, rememberSectionView } from "../store.ts";

export interface ViewDef {
  key: string;
  label: string;
  /** A count shown beside the label (proposals waiting, say); nothing while 0 or absent. */
  count?: number;
  /** The accessible name when the count has to be part of it ("Changes, 3 proposed"); the label
   *  is used when absent. */
  ariaLabel?: string;
}

/** Which view of `section` is showing. `dflt` is the entry's default when nothing was asked or
 *  remembered; it is NOT stored, so a default forced by the state of the workspace never
 *  overwrites what the member chose. */
export function useSectionView(
  section: string,
  views: readonly string[],
  dflt: string,
  useRemembered = true,
): [string, (view: string) => void] {
  // A request names a section too. Only the entry it is addressed to may take or drop it: while
  // the dialog switches entries, the old entry is still mounted for a render and must leave the
  // request for the new one.
  const requested = useSettingsUI((s) => (s.settingsSection === section ? s.settingsView : null));
  const clearRequested = useSettingsUI((s) => s.clearSettingsView);
  const [view, setView] = useState(() => {
    const st = useSettingsUI.getState();
    const ask = st.settingsSection === section ? st.settingsView : null;
    if (ask && views.includes(ask)) return ask;
    const last = useRemembered ? lastSectionView(section) : "";
    return views.includes(last) ? last : dflt;
  });

  const pick = useCallback(
    (v: string) => {
      setView(v);
      rememberSectionView(section, v);
    },
    [section],
  );

  // A request made while this entry is already on screen. A name that is no view of this entry is
  // dropped, so it cannot linger and fire on a later, unrelated mount.
  useEffect(() => {
    if (!requested) return;
    if (views.includes(requested)) pick(requested);
    clearRequested();
  }, [requested, views, pick, clearRequested]);

  return [view, pick];
}

/** A tablist at every width. When the labels do not fit, the row scrolls sideways (memory.css)
 *  instead of wrapping, so the active tab is kept in view. */
export function ViewNav({
  label,
  views,
  value,
  onChange,
  prefix,
}: {
  label: string;
  views: readonly ViewDef[];
  value: string;
  onChange: (view: string) => void;
  /** Id prefix tying each tab to the panel it controls (`${prefix}-panel`). */
  prefix: string;
}) {
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const rowRef = useRef<HTMLDivElement>(null);

  // Scrolls the row itself, never an ancestor: scrollIntoView would also move the dialog body
  // vertically when the view changes from somewhere down the page.
  useEffect(() => {
    const row = rowRef.current;
    const tab = tabRefs.current[value];
    if (!row || !tab) return;
    const r = row.getBoundingClientRect();
    const b = tab.getBoundingClientRect();
    if (b.left < r.left) row.scrollLeft += b.left - r.left;
    else if (b.right > r.right) row.scrollLeft += b.right - r.right;
  }, [value]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    // Leave modified keys (browser/OS shortcuts) and IME composition (keyCode 229) alone.
    if (e.ctrlKey || e.altKey || e.metaKey || e.shiftKey || e.nativeEvent.isComposing || e.keyCode === 229) return;
    const at = views.findIndex((v) => v.key === value);
    let next: number;
    switch (e.key) {
      case "ArrowRight":
        next = (at + 1) % views.length;
        break;
      case "ArrowLeft":
        next = (at - 1 + views.length) % views.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = views.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    const key = views[next].key;
    onChange(key);
    tabRefs.current[key]?.focus({ preventScroll: true });
  };

  return (
    <div className="mem-viewnav">
      <div className="mem-viewnav-tabs" ref={rowRef} role="tablist" aria-label={label} onKeyDown={onKeyDown}>
        {views.map((v) => (
          <button
            key={v.key}
            ref={(el) => {
              tabRefs.current[v.key] = el;
            }}
            type="button"
            role="tab"
            id={`${prefix}-tab-${v.key}`}
            aria-selected={v.key === value}
            aria-controls={`${prefix}-panel`}
            tabIndex={v.key === value ? 0 : -1}
            className={"mem-viewnav-tab" + (v.key === value ? " active" : "")}
            onClick={() => onChange(v.key)}
            aria-label={v.count ? v.ariaLabel : undefined}
          >
            {v.label}
            {v.count ? (
              <span className="mem-viewnav-count" aria-hidden="true">
                {v.count}
              </span>
            ) : null}
          </button>
        ))}
      </div>
    </div>
  );
}
