// WS bar — ported from the old components/WsBar.tsx (docs/log/22 P6a). Verbatim
// except the useApp() reads, which map onto the zustand stores:
//   wsState/startWs/stopWs       → core/store/workspace
//   tenant/superAdmin            → core/store/tenant
//   layout/splitRight/splitDown/resetToTerminal/activePaneId → layout/store
//   openNewSession               → features/sessions/store (tick signal)
import { useEffect, useRef, useState } from "react";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useWorkspaceStore, wsBusy, wsPowerStops, wsStartBusy } from "../../core/store/workspace.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { useIsMobile } from "../../lib/device.ts";
import { useWsBarFold, useWsBarPhoneFold } from "../wsBarFold.ts";
import { machineSummary } from "../../lib/machine.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { hintSuffix } from "../../features/keys/keyHint.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { fmtGiB as fg } from "../../lib/bytes.ts";
import { AwsProfilesChip } from "../../features/awslogin/AwsProfilesChip.tsx";
import { GcpProfilesChip } from "../../features/gcplogin/GcpProfilesChip.tsx";
import { RemainingUsageChip } from "./RemainingUsageChip.tsx";
import { SlotMoveNotice } from "./SlotMoveNotice.tsx";
import { UsageChip } from "./UsageChip.tsx";
import { MachineDetailsLink, useWsMachine } from "./machine.tsx";
import { DesktopMore, MobileMore } from "./MoreMenu.tsx";
import { useUsageFeed } from "./usageFeed.ts";
import { StaleBadge } from "./StaleBadge.tsx";
import { useLayoutButtons } from "./useLayoutButtons.tsx";
import { usePortPreview } from "./usePortPreview.tsx";
import { useWsPower } from "./useWsPower.ts";
import { useWsResourceChips } from "./resourceStats.ts";
import { UsageChipFold } from "./usageFold.tsx";
import { REMAINING_SOURCES, USAGE_SOURCES } from "./usageSources.ts";

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "recreate" lives deep in Settings > Environment (warning-gated), off this bar.

// On desktop the resource chips / opencode web / port-preview sit inline at the
// right; on a phone they'd wrap to a second line, so they fold into a single ⋯
// overflow popover instead.
// Friendly label for the raw container state. The CP returns runtime-derived
// states (runtime.go State()): "running" | "starting" | "stopped" | "none".
// "starting" is server-reported (ECS: the workspace image cold-pulls for minutes
// before the task runs) — shown as "starting" and walked to "running" by the 4s poll, no
// reload needed. Stop does `docker rm -f` locally, so the *normal* stopped state
// is "none" (no container — data persists in the bind mount, recreated on Start);
// "stopped" only appears when a container exists but exited on its own (crash /
// OOM) or the ECS service sits at desired 0. Both read as "stopped" to the user; the
// raw state stays in the tooltip. Optimistic in-flight states (set by the store
// around start/stop POSTs) end in "…".
// `reason` is why the state could not be read (workspace store). Left as "unknown" the bar
// can only say the fetch failed, which leaves a super_admin who has not been invited yet
// with an "unknown" that names no cause.
function wsLabel(s: string, reason = ""): string {
  if (s === "unknown" && reason === "not_provisioned") return t("wsbar.state.no_tenant");
  switch (s) {
    case "running":
      return t("wsbar.state.running");
    case "starting":
      return t("wsbar.state.starting");
    case "none":
    case "stopped":
      return t("wsbar.state.stopped");
    case "starting…":
      return t("wsbar.state.starting");
    case "stopping…":
      return t("wsbar.state.stopping");
    case "recreating…":
      return t("wsbar.state.recreating");
    case "unknown":
      return t("wsbar.state.unknown");
    default:
      return s; // "…" initial, or any future state
  }
}

// One resource tile: label + trend sparkline + current value, tinted by level
// (0 normal / 1 warn / 2 crit). Called as a plain function (not <Tile/>) so it's
// just inline JSX, no extra component instance.
function tile({
  k,
  series,
  max,
  track,
  value,
  level,
  title,
}: {
  k: string;
  series: (number | null)[];
  max?: number;
  track?: boolean;
  value: string;
  level: number;
  title: string;
}) {
  return (
    // The key is the static k only: title carries live values (memory use and the like),
    // so folding it in would remount on every change and make the sparkline flicker.
    <span className={"ws-graph" + (level === 2 ? " crit" : level === 1 ? " warn" : "")} title={title} key={k}>
      <span className="ws-graph-k">{k}</span>
      <Sparkline data={series} max={max} track={track} />
      <span className="ws-graph-v">{value}</span>
    </span>
  );
}

export function WsBar() {
  const wsState = useWorkspaceStore((s) => s.state);
  const wsStale = useWorkspaceStore((s) => s.stale);
  const wsSlotReplace = useWorkspaceStore((s) => s.slotReplace);
  const wsReason = useWorkspaceStore((s) => s.reason);
  const tenant = useTenantStore((s) => s.tenant);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  // Belonging to no tenant means no workspace exists at all. Only a super_admin who has
  // not been invited yet reaches this (everyone else lands on NotProvisioned), so the
  // right guidance is to add themselves to a tenant from Admin.
  const noTenant = wsState === "unknown" && wsReason === "not_provisioned";
  const tr = useT();
  const { wsStats, wsHist, hostStats, hostHist } = useWsResourceChips(tenant, superAdmin);
  useUsageFeed(tenant);
  const isMobile = useIsMobile();
  const barRef = useRef<HTMLDivElement>(null);
  const { foldUsage, foldMore, noteUsageLayout } = useWsBarFold(barRef, !isMobile);
  const phoneFold = useWsBarPhoneFold(barRef, isMobile);
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  const [moreOpen, setMoreOpen] = useState(false); // ⋯ overflow popover (phone, or a full desktop bar)
  const [resOpen, setResOpen] = useState(false); // desktop resource-tiles popover
  // Keyboard: Ctrl/⌘+K g r toggles the resource-tiles popover (desktop). While the bar has
  // folded the resources chip into ⋯, the tiles are in there, and a popover anchored on a
  // hidden chip would open invisibly.
  useOpenSignal("resources", () => (foldMore ? setMoreOpen((o) => !o) : setResOpen((o) => !o)));
  // A fold step hides the anchor of whichever popover is on the wrong side of it. A hidden
  // popover's dismiss layer would still swallow the next click anywhere, so close it.
  useEffect(() => {
    if (isMobile) return;
    if (foldMore) {
      setResOpen(false);
      setPvOpen(false);
    } else setMoreOpen(false);
  }, [foldMore, isMobile]);
  const machine = useWsMachine(resOpen, tenant, wsState === "running");
  const machineLine = machine ? machineSummary(machine) : "";
  const pvRef = useRef<HTMLDivElement>(null);
  const resRef = useRef<HTMLDivElement>(null);
  const running = wsState === "running";
  // Toggle inert while a transition is in flight: the optimistic "…" states AND the
  // server-reported "starting" (ECS cold pull — a second Start click must not
  // re-drive the deployment; the 4s poll flips the bar to "running" on its own).
  const busy = wsStartBusy(wsState);
  // …but only the OPTIMISTIC "…" states may disable the POWER button — those are
  // momentary (one request in flight). The server-reported "starting" is not, and it
  // must stay clickable so a wedged start can be cancelled (wsPowerStops).
  const inFlight = wsBusy(wsState);
  const powerStops = wsPowerStops(wsState);

  const { startQueued, onStart, onToggle, onRestart } = useWsPower(running, busy, powerStops);
  const { splitRightBtn, splitDownBtn, closeAllBtn, homeBtn, overviewBtn, imagegenBtn } = useLayoutButtons(isMobile);

  // Backend drift (CP-detected): the running container predates the deployed build,
  // so a stop→start would pick up the new backend. Shown only while genuinely
  // running — never mid-transition, where the restart is already happening. It is a
  // state, not an event: it clears itself once the workspace is back on the current
  // build, so there is no dismiss.
  const staleShown = wsStale && running && !busy;
  // ecs-ec2: an administrator retired the machine this workspace sits on (#1473). Shown
  // whether running or stopped, hidden while a start (the move itself) runs.
  const slotMoveShown = wsSlotReplace && !busy;
  const previewPop = usePortPreview(pvOpen, moreOpen, running, setPvOpen, setMoreOpen);

  // Close each popover on an outside click / Escape.
  useDismiss(pvRef, pvOpen, () => setPvOpen(false));
  useDismiss(resRef, resOpen, () => setResOpen(false));

  // --- pieces shared between the inline (desktop) and folded (mobile) layouts ---
  const lvl = (v: number | null, warn: number, crit: number) => (v == null ? 0 : v >= crit ? 2 : v >= warn ? 1 : 0);

  // Container (own workspace): memory fill (vs quota) + CPU%. Shown to everyone.
  const hasWs = wsStats && wsStats.running && wsStats.mem_used != null;
  // hasWs already guarantees mem_used != null, but TS property narrowing does not reach
  // through the alias, so ?? 0 spells it out (an unreachable fallback).
  const memRatio = hasWs && wsStats.mem_max ? (wsStats.mem_used ?? 0) / wsStats.mem_max : null;
  // A process in this container was OOM-killed within the last few minutes (the
  // container itself survived). Flag the memory tile crit so it's noticed even after
  // usage falls back — a build/agent likely just died. (metrics.go oom_recent.)
  const oomRecent = !!(hasWs && wsStats.oom_recent);
  const containerTiles = hasWs && (
    <>
      {tile({
        k: "mem",
        series: wsHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: memRatio != null ? `${Math.round(memRatio * 100)}%` : `${fg(wsStats.mem_used ?? 0)}G`,
        level: oomRecent ? 2 : lvl(memRatio, 0.75, 0.9),
        title: oomRecent
          ? tr("wsbar.tile.ws_mem", { mem: `${fg(wsStats.mem_used ?? 0)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}` }) +
            "\n" +
            tr("wsbar.tile.ws_mem_oom_note")
          : tr("wsbar.tile.ws_mem", { mem: `${fg(wsStats.mem_used ?? 0)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}` }),
      })}
      {tile({
        k: "cpu",
        series: wsHist.map((p) => p.cpu),
        value: wsStats.cpu_pct != null ? `${Math.round(wsStats.cpu_pct)}%` : "–",
        level: lvl(wsStats.cpu_pct ?? null, 60, 90),
        title: tr("wsbar.tile.ws_cpu"),
      })}
    </>
  );

  // Host (super_admin only): load average (normalized to cores) + total memory fill.
  const hasHost = hostStats && hostStats.mem_total != null;
  const loadNorm = hasHost && hostStats.ncpu ? hostStats.load1 / hostStats.ncpu : null;
  const hostMemRatio = hasHost ? hostStats.mem_used / hostStats.mem_total : null;
  const hostTiles = hasHost && (
    <>
      {tile({
        k: "ld",
        series: hostHist.map((p) => p.load),
        max: 1,
        track: true,
        value: Number(hostStats.load1).toFixed(2),
        level: lvl(loadNorm, 0.7, 1),
        title: tr("wsbar.tile.host_load", { load: Number(hostStats.load1).toFixed(2), ncpu: hostStats.ncpu ?? "?" }),
      })}
      {tile({
        k: "mem",
        series: hostHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: hostMemRatio != null ? `${Math.round(hostMemRatio * 100)}%` : "",
        level: lvl(hostMemRatio, 0.8, 0.92),
        title: tr("wsbar.tile.host_mem", { mem: `${fg(hostStats.mem_used)}/${fg(hostStats.mem_total)}` }),
      })}
    </>
  );

  const graphs = (containerTiles || hostTiles) && (
    <>
      {containerTiles}
      {containerTiles && hostTiles && <span className="ws-graph-sep" />}
      {hostTiles}
    </>
  );

  // Desktop: collapse the resource sparkline tiles behind one "resources" chip so the
  // bar isn't a wall of tiles. The chip shows a glanceable container summary
  // (mem/cpu %) and opens a popover with the full trend tiles (incl. host for
  // super_admin). On mobile the tiles already live in the ⋯ overflow, so this is
  // desktop-only (graphs is reused there).
  const resSummary = hasWs
    ? `mem ${memRatio != null ? Math.round(memRatio * 100) : "–"}% · cpu ${
        wsStats.cpu_pct != null ? Math.round(wsStats.cpu_pct) : "–"
      }%`
    : null;
  const resourcesEl = graphs && (
    <div className="ws-res" ref={resRef}>
      <Button
        variant="ghost"
        className="ws-res-btn"
        title={tr("wsbar.resources_title")}
        aria-expanded={resOpen}
        onClick={() => setResOpen((o) => !o)}
      >
        <Icon name="pulse" />
        <span className="ws-res-sum">{resSummary || tr("wsbar.resources")}</span>
        <Icon name="chevron-down" />
      </Button>
      {resOpen && (
        <div className="ws-res-pop">
          {/* What the numbers below are being spent ON. One line, because the popover
              answers "how am I doing" and this is the scale that question is relative to;
              the full breakdown is Settings › Machine. */}
          {machineLine && (
            <div className="ws-res-machine" title={tr("wsbar.machine_title")}>
              {machineLine}
            </div>
          )}
          {graphs}
          <MachineDetailsLink onNavigate={() => setResOpen(false)} />
        </div>
      )}
    </div>
  );

  // Subscription-usage chips, one per agent that exposes limits (Claude, Codex, muse,
  // Copilot, Antigravity — display order matches the agent order). Each self-hides until
  // its endpoint answers, so a claude-only user sees one chip, a user of all sees five.
  // The 8px bar gap spaces them (no manual dividers needed).
  const usageChips = (
    <>
      {USAGE_SOURCES.map((s) => (
        <UsageChip key={s.endpoint} src={s} tenant={tenant} />
      ))}
      {REMAINING_SOURCES.map((s) => (
        <RemainingUsageChip key={s.endpoint} src={s} tenant={tenant} />
      ))}
    </>
  );

  const statsBlock = (
    <>
      {graphs}
      {/* Mobile gets the same hand-over as the desktop popover. Gated on `graphs` so that a
          workspace with no tiles adds no child here — .ws-more-stats:empty is what collapses
          the block's padding, and an unconditional link would defeat it. */}
      {graphs && <MachineDetailsLink onNavigate={() => setMoreOpen(false)} />}
      {usageChips}
      <AwsProfilesChip />
      <GcpProfilesChip />
    </>
  );

  return (
    <div className="wsbar" ref={barRef}>
      <span className="ws-label">
        <span className="lbl">Workspace</span>
        <span className="lbl-short">WS</span>
      </span>
      {/* Power toggle: a single icon-only ⏻ to the LEFT of the state chip — the chip
          says running/stopped, this starts or stops accordingly (no separate labeled
          button). The bar auto-syncs wsState from the 4s workspace poll, so an
          externally-changed workspace (admin stop / OOM) reflects without a manual
          refresh. Disabled mid-transition (starting…/stopping…), where it shows a
          spinner instead of the glyph. */}
      <Button
        className={"ws-power " + (running ? "on" : powerStops ? "starting" : "off") + (staleShown ? " stale" : "")}
        onClick={onToggle}
        disabled={inFlight}
        title={
          noTenant
            ? tr(superAdmin ? "wsbar.state_title.no_tenant_admin" : "wsbar.state_title.no_tenant")
            : (powerStops ? tr("wsbar.stop_ws") : tr("wsbar.start_ws")) + (staleShown ? " — " + tr("wsbar.stale.title") : "")
        }
        aria-label={powerStops ? tr("wsbar.stop_ws") : tr("wsbar.start_ws")}
      >
        {inFlight ? (
          <Icon name="loading" spin />
        ) : (
          // A server-reported "starting" keeps the glyph (amber, see .ws-power.starting): a
          // spinner there read as "wait", hiding that the button cancels a wedged start.
          // Inline SVG power symbol — the Unicode ⏻ (U+23FB) is missing from many
          // mobile system fonts (renders blank on phones), so draw it instead. Inherits
          // currentColor, so the state/hover color classes still apply.
          <svg
            className="ws-power-glyph"
            viewBox="0 0 24 24"
            width="15"
            height="15"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M12 3.5v8" />
            <path d="M7.3 6.7a7 7 0 1 0 9.4 0" />
          </svg>
        )}
      </Button>
      <span className={"ws-dot " + (running ? "on" : "off")}>●</span>
      <span
        className={"ws-state" + (wsState === "stopped" && wsStats?.oom_killed ? " warn" : "")}
        title={
          noTenant
            ? tr(superAdmin ? "wsbar.state_title.no_tenant_admin" : "wsbar.state_title.no_tenant")
            : wsState === "none"
            ? tr("wsbar.state_title.none")
            : wsState === "stopped"
              ? wsStats?.oom_killed
                ? tr("wsbar.state_title.oom", { code: wsStats.exit_code ?? "?" })
                : wsStats?.exit_code
                  ? tr("wsbar.state_title.exited", { code: wsStats.exit_code })
                  : tr("wsbar.state_title.stopped")
              : wsState === "starting"
                ? tr("wsbar.state_title.starting")
                : tr("wsbar.state_title.other", { state: wsState })
        }
      >
        {wsLabel(wsState, wsReason)}
      </span>
      {staleShown && <StaleBadge onRestart={onRestart} />}
      {slotMoveShown && <SlotMoveNotice />}
      {/* The single "start anything" entry (launch flow Ph2): opens the StartModal hub
          (chat / repo / clone / home / other). While the workspace
          is stopped it offers to start it and opens the hub when ready (Ph3). */}
      <Button
        variant="ghost"
        className="ws-split ws-newsession"
        title={
          running
            ? tr("wsbar.start_here.running") + hintSuffix("session.new")
            : startQueued
              ? tr("wsbar.start_here.queued")
              : tr("wsbar.start_here.stopped")
        }
        aria-label={tr("wsbar.start_here")}
        onClick={() => void onStart()}
      >
        <Icon name={startQueued ? "loading" : "add"} spin={startQueued} />
        <span className="lbl">{tr("wsbar.start_here")}</span>
      </Button>
      {splitRightBtn}
      {splitDownBtn}
      {closeAllBtn}
      {/* The sessions overview (ADR 0078). It sits with the pane buttons because it IS a
          pane; the rail's layout map has the same button, but that map hides itself while
          there is a single pane — which is exactly when someone reaches for the overview.
          Home (#2028) goes first: it is the way back to "what is next" from anywhere. */}
      {homeBtn}
      {overviewBtn}
      {/* The image-generation studio (ADR 0081), beside the overview for the same reason:
          it is a pane, and the layout map's copy of this button hides itself while there is
          only one pane. Shown only while the fleet has an image engine to offer (decision 1):
          a button that opens onto "the engine cannot be used" is a promise the bar cannot
          keep, and most fleets have no engine at all. */}
      {imagegenBtn}

      <span className="ws-spacer" />

      {isMobile ? (
        <MobileMore
          open={moreOpen}
          setOpen={setMoreOpen}
          phoneFold={phoneFold}
          splitDownBtn={splitDownBtn}
          closeAllBtn={closeAllBtn}
          homeBtn={homeBtn}
          overviewBtn={overviewBtn}
          imagegenBtn={imagegenBtn}
          statsBlock={statsBlock}
          previewPop={previewPop}
        />
      ) : (
        <>
          {/* Desktop only: on a phone these already sit in the ⋯ overflow, a vertical list
              with room for all of them — folding a list into a list would only bury them. */}
          <UsageChipFold squeeze={foldUsage} onLayoutChange={noteUsageLayout}>
            {usageChips}
          </UsageChipFold>
          {/* Outside the fold: that group ranks agents by use, and these are not agents. */}
          <AwsProfilesChip hidden={foldMore} />
          <GcpProfilesChip hidden={foldMore} />
          {resourcesEl}
          <div className="ws-preview" ref={pvRef}>
            <Button
              variant="ghost"
              className="ws-preview-btn"
              disabled={!running}
              title={tr("wsbar.preview_title")}
              aria-expanded={pvOpen}
              onClick={() => setPvOpen((o) => !o)}
            >
              <Icon name="globe" /> {tr("wsbar.preview")} <Icon name="chevron-down" />
            </Button>
            {pvOpen && <div className="ws-preview-pop">{previewPop}</div>}
          </div>
          {/* The same four, folded behind ⋯ once the bar runs out of width (wsBarFold.ts).
              The chips above stay mounted, hidden by CSS, so nothing re-fetches as the
              window crosses the boundary; the popover mounts its copies only while open. */}
          <DesktopMore open={moreOpen} setOpen={setMoreOpen} foldMore={foldMore}>
            {/* statsBlock without the usage chips: those keep their own +N group. */}
            <div className="ws-more-stats">
              {graphs}
              {graphs && <MachineDetailsLink onNavigate={() => setMoreOpen(false)} />}
              <AwsProfilesChip passive />
              <GcpProfilesChip passive />
            </div>
            {previewPop}
          </DesktopMore>
        </>
      )}
    </div>
  );
}
