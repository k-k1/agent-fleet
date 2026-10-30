import { useLayoutStore } from "../../layout/store.ts";
import { isBlankPane, MAX_TAB_COLS } from "../../layout/ops.ts";
import { openSessionsOverview } from "../../features/overview/open.ts";
import { openHome } from "../../features/home/open.ts";
import { openImagegen } from "../../features/imagegen/open.ts";
import { useImagegenAvailable } from "../../features/imagegen/available.ts";
import { hintSuffix } from "../../features/keys/keyHint.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

// The pane buttons of the bar. The ones that fold into ⋯ on a narrow phone (wsBarFold.ts,
// useWsBarPhoneFold) are rendered from these elements in both places, so the bar's copy and the
// popover's copy cannot drift.
export function useLayoutButtons(isMobile: boolean) {
  const imagegenAvailable = useImagegenAvailable();
  const layout = useLayoutStore((s) => s.layout);
  const splitRight = useLayoutStore((s) => s.splitRight);
  const splitDown = useLayoutStore((s) => s.splitDown);
  const resetToTerminal = useLayoutStore((s) => s.resetToTerminal);
  const tr = useT();
  const activePaneId = layout.activeCellId;

  // "Close all panes" collapses the split layout back to one empty terminal pane.
  // Disabled when there's already just a single empty pane (nothing to close).
  const totalPanes = layout.cols.reduce((n, c) => n + c.cells.length, 0);
  const onlyPane = totalPanes === 1 ? layout.cols[0].cells[0] : null;
  const canCloseAll = !(onlyPane && isBlankPane(onlyPane));

  // Split (moved here from the per-pane header): split-right appends a column (global);
  // split-down splits the ACTIVE pane's column into rows. Same limits as before — up to
  // 4 columns (desktop only), each column up to 2 panes. Mobile: one top/bottom split.
  const activeCol = layout.cols.find((c) => c.cells.some((p) => p.id === activePaneId));
  const canSplitRight = !isMobile && layout.cols.length < (layout.mode === "tabs" ? MAX_TAB_COLS : 4);
  const canSplitDown = isMobile ? totalPanes < 2 : (activeCol ? activeCol.cells.length : totalPanes) < 2;

  // Columns are a desktop layout: canSplitRight is false for the whole phone width, so
  // the button could only ever sit there disabled — and the bar has no room to spare
  // (an overflowing bar widens the page, see wsbar.css). Dropped rather than disabled;
  // it returns with the width, since useIsMobile tracks the media query.
  const splitRightBtn = !isMobile && (
    <Button
      variant="ghost"
      className="ws-split"
      title={tr("wsbar.split_right") + hintSuffix("pane.splitRight")}
      aria-label={tr("wsbar.split_right")}
      disabled={!canSplitRight}
      onClick={() => splitRight()}
    >
      <Icon name="split-horizontal" />
      <span className="lbl">{tr("wsbar.split_right")}</span>
    </Button>
  );
  const splitDownBtn = (
    <Button
      variant="ghost"
      className="ws-split"
      title={tr("wsbar.split_down_title") + hintSuffix("pane.splitDown")}
      aria-label={tr("wsbar.split_down")}
      disabled={!canSplitDown}
      onClick={() => activePaneId && splitDown(activePaneId)}
    >
      <Icon name="split-vertical" />
      <span className="lbl">{tr("wsbar.split_down")}</span>
    </Button>
  );
  const closeAllBtn = (
    <Button
      variant="ghost"
      className="ws-closeall"
      title={tr("wsbar.close_all_title") + hintSuffix("pane.closeAll")}
      aria-label={tr("wsbar.close_all")}
      disabled={!canCloseAll}
      onClick={() => resetToTerminal()}
    >
      <Icon name="close-all" />
      <span className="lbl">{tr("wsbar.close_all")}</span>
    </Button>
  );
  const homeBtn = (
    <Button
      variant="ghost"
      className="ws-split ws-home"
      title={tr("wsbar.home_title") + hintSuffix("open.home")}
      aria-label={tr("wsbar.home")}
      onClick={() => openHome()}
    >
      <Icon name="home" />
      <span className="lbl">{tr("wsbar.home")}</span>
    </Button>
  );
  const overviewBtn = (
    <Button
      variant="ghost"
      className="ws-split ws-overview"
      title={tr("wsbar.overview_title") + hintSuffix("open.sessions")}
      aria-label={tr("wsbar.overview")}
      onClick={() => openSessionsOverview()}
    >
      <Icon name="dashboard" />
      <span className="lbl">{tr("wsbar.overview")}</span>
    </Button>
  );
  const imagegenBtn = imagegenAvailable && (
    <Button
      variant="ghost"
      className="ws-split ws-imagegen"
      title={tr("wsbar.imagegen_title") + hintSuffix("open.imagegen")}
      aria-label={tr("wsbar.imagegen")}
      onClick={() => void openImagegen()}
    >
      <Icon name="wand" />
      <span className="lbl">{tr("wsbar.imagegen")}</span>
    </Button>
  );

  return { splitRightBtn, splitDownBtn, closeAllBtn, homeBtn, overviewBtn, imagegenBtn };
}
