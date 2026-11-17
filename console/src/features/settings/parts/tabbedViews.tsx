// A settings page split into views by a ViewNav, with every view kept mounted. Only the active
// view is shown; the others are hidden, never unmounted, so a draft or a refusal in one view
// survives a switch to another, and the tab of a view with unsaved edits carries a mark.
import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { DirtyScope } from "./dirtyScope.tsx";
import { ViewNav } from "./viewNav.tsx";

export interface TabbedView {
  key: string;
  label: string;
  node: ReactNode;
  /** false keeps the view mounted but leaves its tab out, for a view whose content decides
   *  whether it exists (provided routes on a CP that does not serve them). */
  shown?: boolean;
}

// What a view may ask of the page it sits on: switch to another view (a banner that points at the
// tab where its cause can be changed), and whether it is the one showing (a view that reads a lot
// waits until it is first opened). Outside a TabbedViews, there is nowhere to switch to and every
// view counts as showing.
const TabsContext = createContext<{ show: (key: string) => void; has: (key: string) => boolean } | null>(null);
const ActiveContext = createContext(true);

/** useShowView: switch to the view `key`, or null where there is no such view to switch to. */
export function useShowView(key: string): (() => void) | null {
  const tabs = useContext(TabsContext);
  return tabs && tabs.has(key) ? () => tabs.show(key) : null;
}

/** useViewActive: whether the view holding the caller is the one showing. */
export const useViewActive = () => useContext(ActiveContext);

export function TabbedViews({ label, prefix, views }: { label: string; prefix: string; views: readonly TabbedView[] }) {
  const tr = useT();
  const [dirty, setDirty] = useState<Record<string, boolean>>({});
  // A view holding unsaved edits keeps its tab even when it says it should not exist, so the
  // edits are never left where no tab leads.
  const hasTab = (v: TabbedView) => v.shown !== false || !!dirty[v.key];
  const shown = views.filter(hasTab);
  const [picked, setPicked] = useState(views[0]?.key ?? "");
  const value = shown.some((v) => v.key === picked) ? picked : (shown[0]?.key ?? "");
  const keys = views.map((v) => v.key).join("\n");
  // One stable callback per view: DirtyScope re-reports whenever its onChange changes.
  const onDirty = useMemo(
    () =>
      Object.fromEntries(
        keys.split("\n").map((k) => [k, (d: boolean) => setDirty((p) => (!!p[k] === d ? p : { ...p, [k]: d }))]),
      ),
    [keys],
  );
  const tabs = useMemo(
    () => ({ show: setPicked, has: (k: string) => shown.some((v) => v.key === k) }),
    // shown is rebuilt each render; its keys are what matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [shown.map((v) => v.key).join("\n")],
  );
  return (
    <TabsContext.Provider value={tabs}>
      <ViewNav
        label={label}
        prefix={prefix}
        panelPerView
        dirtyLabel={tr("common.unsaved_changes")}
        views={shown.map((v) => ({ key: v.key, label: v.label, dirty: !!dirty[v.key] }))}
        value={value}
        onChange={setPicked}
      />
      {views.map((v) => (
        <div
          key={v.key}
          role="tabpanel"
          id={`${prefix}-panel-${v.key}`}
          // A view without a tab names none: the id would point at nothing.
          aria-labelledby={hasTab(v) ? `${prefix}-tab-${v.key}` : undefined}
          hidden={v.key !== value}
          data-view={v.key}
          className="tabbed-view"
        >
          <ActiveContext.Provider value={v.key === value}>
            <DirtyScope onChange={onDirty[v.key]}>{v.node}</DirtyScope>
          </ActiveContext.Provider>
        </div>
      ))}
    </TabsContext.Provider>
  );
}
