import { useRef } from "react";
import type { ReactNode } from "react";
import type { RailView as RailViewId } from "../core/store/leftRail.ts";
import { ViewShownContext } from "../lib/viewShown.ts";
import { WorkingSetBar } from "./WorkingSetBar.tsx";

// One view of the left pane. Hidden rather than unmounted while another view is on screen.
// `shown` is whether the pane itself is on screen (folded away, or the phone drawer closed,
// hides every view); the view's content reads the result through ViewShownContext. `lazy`
// defers mounting the content to the first time the view is on screen.
export function RailView({
  id,
  active,
  shown = true,
  scoped,
  lazy,
  children,
}: {
  id: RailViewId;
  active: RailViewId;
  shown?: boolean;
  scoped?: boolean;
  lazy?: boolean;
  children: ReactNode;
}) {
  const onScreen = id === active && shown;
  const seen = useRef(false);
  if (onScreen) seen.current = true;
  return (
    <div className="rail-view" data-rail-view={id} hidden={id !== active}>
      {scoped && <WorkingSetBar />}
      <div className="app-rail-scroll">
        <ViewShownContext.Provider value={onScreen}>{lazy && !seen.current ? null : children}</ViewShownContext.Provider>
      </div>
    </div>
  );
}
