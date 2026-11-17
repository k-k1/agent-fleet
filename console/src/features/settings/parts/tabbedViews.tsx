// A settings page split into views by a ViewNav, with every view kept mounted. Only the active
// view is shown; the others are hidden, never unmounted, so a draft or a refusal in one view
// survives a switch to another, and the tab of a view with unsaved edits carries a mark.
import { useMemo, useState, type ReactNode } from "react";
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

export function TabbedViews({ label, prefix, views }: { label: string; prefix: string; views: readonly TabbedView[] }) {
  const tr = useT();
  const shown = views.filter((v) => v.shown !== false);
  const [picked, setPicked] = useState(views[0]?.key ?? "");
  const value = shown.some((v) => v.key === picked) ? picked : (shown[0]?.key ?? "");
  const [dirty, setDirty] = useState<Record<string, boolean>>({});
  const keys = views.map((v) => v.key).join("\n");
  // One stable callback per view: DirtyScope re-reports whenever its onChange changes.
  const onDirty = useMemo(
    () =>
      Object.fromEntries(
        keys.split("\n").map((k) => [k, (d: boolean) => setDirty((p) => (!!p[k] === d ? p : { ...p, [k]: d }))]),
      ),
    [keys],
  );
  return (
    <>
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
          aria-labelledby={`${prefix}-tab-${v.key}`}
          hidden={v.key !== value}
          data-view={v.key}
          className="tabbed-view"
        >
          <DirtyScope onChange={onDirty[v.key]}>{v.node}</DirtyScope>
        </div>
      ))}
    </>
  );
}
