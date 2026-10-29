// WS bar — ported from the old components/WsBar.tsx (docs/log/22 P6a). Verbatim
// except the useApp() reads, which map onto the zustand stores:
//   wsState/startWs/stopWs       → core/store/workspace
//   tenant/superAdmin            → core/store/tenant
//   layout/splitRight/splitDown/resetToTerminal/activePaneId → layout/store
//   openNewSession               → features/sessions/store (tick signal)
import { useEffect, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useWorkspaceStore, wsBusy, wsPowerStops, wsStartBusy } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { openSessionsOverview } from "../../features/overview/open.ts";
import { openHome } from "../../features/home/open.ts";
import { openImagegen } from "../../features/imagegen/open.ts";
import { useImagegenAvailable } from "../../features/imagegen/available.ts";
import { isBlankPane, MAX_TAB_COLS } from "../../layout/ops.ts";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { hintSuffix } from "../../features/keys/keyHint.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useIsMobile } from "../../lib/device.ts";
import { useWsBarFold, useWsBarPhoneFold } from "../wsBarFold.ts";
import { machineSummary } from "../../lib/machine.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { listBrowserAttachments } from "../../features/browser/attachmentService.ts";
import { openBrowserAttachment } from "../../features/browser/attachmentAction.ts";
import type { BrowserAttachmentStatus } from "../../features/browser/attachmentController.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { fmtGiB as fg } from "../../lib/bytes.ts";
import { PortOpen, useDefaultPortOpen } from "../../features/preview/PortOpen.tsx";
import { useCurrentPreviewInfo, usePreviewStore } from "../../features/preview/previewState.ts";
import { useBrowserUnavailable } from "../../features/browser/availability.ts";
import { AwsProfilesChip } from "../../features/awslogin/AwsProfilesChip.tsx";
import { GcpProfilesChip } from "../../features/gcplogin/GcpProfilesChip.tsx";
import { RemainingUsageChip } from "./RemainingUsageChip.tsx";
import { SlotMoveNotice } from "./SlotMoveNotice.tsx";
import { UsageChip } from "./UsageChip.tsx";
import { MachineDetailsLink, useWsMachine } from "./machine.tsx";
import { useWsResourceChips } from "./resourceStats.ts";
import { UsageChipFold } from "./usageFold.tsx";
import { REMAINING_SOURCES, USAGE_SOURCES } from "./usageSources.ts";

// A stable empty list: a selector returning a fresh [] each time re-renders forever.
const NO_PORTS: number[] = [];

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "recreate" lives deep in Settings > Environment (warning-gated), off this bar.
// SharedPreview is one row of GET /api/preview/shared (docs/log/81 §14.6). `running` means
// "a preview URL exists right now", not the container's own state.
type SharedPreview = {
  ownerUserKey: string;
  ownerEmail: string;
  ports: number[];
  urls: Record<string, string>;
  shareLinks: Record<string, string>;
  running: boolean;
};

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
  const imagegenAvailable = useImagegenAvailable();
  const startWs = useWorkspaceStore((s) => s.start);
  const stopWs = useWorkspaceStore((s) => s.stop);
  const restartWs = useWorkspaceStore((s) => s.restart);
  const wsStale = useWorkspaceStore((s) => s.stale);
  const wsSlotReplace = useWorkspaceStore((s) => s.slotReplace);
  const wsReason = useWorkspaceStore((s) => s.reason);
  const tenant = useTenantStore((s) => s.tenant);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  // Belonging to no tenant means no workspace exists at all. Only a super_admin who has
  // not been invited yet reaches this (everyone else lands on NotProvisioned), so the
  // right guidance is to add themselves to a tenant from Admin.
  const noTenant = wsState === "unknown" && wsReason === "not_provisioned";
  const layout = useLayoutStore((s) => s.layout);
  const splitRight = useLayoutStore((s) => s.splitRight);
  const splitDown = useLayoutStore((s) => s.splitDown);
  const resetToTerminal = useLayoutStore((s) => s.resetToTerminal);
  const activePaneId = layout.activeCellId;
  const openStart = useSessionsStore((s) => s.openStart);
  const askConfirm = useConfirm();
  const tr = useT();
  const { wsStats, wsHist, hostStats, hostHist } = useWsResourceChips(tenant, superAdmin);
  const isMobile = useIsMobile();
  const barRef = useRef<HTMLDivElement>(null);
  const { foldUsage, foldMore, noteUsageLayout } = useWsBarFold(barRef, !isMobile);
  const phoneFold = useWsBarPhoneFold(barRef, isMobile);
  // Unlisted ports the owner has open right now (control-plane/preview_grant.go).
  const tempPorts = useCurrentPreviewInfo()?.tempPorts ?? NO_PORTS;
  const [port, setPort] = useState("");
  const [previewPath, setPreviewPath] = useState("/");
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  // Live Chromium attachments, read when the popover opens (no polling: this is
  // a recovery entry, and the authoritative state is the pane's own socket).
  const [attachments, setAttachments] = useState<BrowserAttachmentStatus[]>([]);
  // The issued preview subdomain (docs/log/81). It changes on every start, so re-read it
  // the moment the popover opens. On a deployment without host-based previews
  // previewDomain is empty and nothing is shown.
  const [pvHosts, setPvHosts] = useState<{
    domain: string;
    urls: Record<string, string>;
    // A pasteable link that survives restarts (docs/log/81 §14.6); present while stopped too.
    shareLinks: Record<string, string>;
    public: boolean;
    tenantShare: boolean;
  } | null>(null);
  // Previews shared by other people in the same tenant (docs/log/81 §14.6).
  const [pvShared, setPvShared] = useState<SharedPreview[]>([]);
  const [staleOpen, setStaleOpen] = useState(false); // "restart required" badge popover
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
  const staleRef = useRef<HTMLDivElement>(null);
  const moreRef = useRef<HTMLDivElement>(null);
  const resRef = useRef<HTMLDivElement>(null);
  const running = wsState === "running";
  const browserUnavailable = useBrowserUnavailable();
  // Toggle inert while a transition is in flight: the optimistic "…" states AND the
  // server-reported "starting" (ECS cold pull — a second Start click must not
  // re-drive the deployment; the 4s poll flips the bar to "running" on its own).
  const busy = wsStartBusy(wsState);
  // …but only the OPTIMISTIC "…" states may disable the POWER button — those are
  // momentary (one request in flight). The server-reported "starting" is not, and it
  // must stay clickable so a wedged start can be cancelled (wsPowerStops).
  const inFlight = wsBusy(wsState);
  const powerStops = wsPowerStops(wsState);

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

  // The "start" entry while stopped: don't dead-end (launch flow Ph3) — confirm, start the
  // workspace, and open the hub once the 4s poll reports running. startQueued
  // survives the whole starting window; a second click while queued re-confirms
  // harmlessly (startWs is only re-fired from a genuinely stopped state).
  const [startQueued, setStartQueued] = useState(false);
  useEffect(() => {
    if (!startQueued) return;
    if (running) {
      setStartQueued(false);
      openStart();
    } else if (!busy) {
      // Settled on stopped/none/unknown (start failed, stopped externally, …): it will
      // never reach running now, so without clearing here the start button sticks on its
      // spinner. Right after the click start() queues "starting…" in the same batch, so
      // this cannot clear it early.
      setStartQueued(false);
    }
  }, [startQueued, running, busy, openStart]);
  const onStart = async () => {
    if (running) {
      openStart();
      return;
    }
    const ok = await askConfirm({
      title: tr("wsbar.confirm.start.title"),
      body: tr("wsbar.confirm.start.body"),
      confirmLabel: busy ? tr("wsbar.confirm.start.wait") : tr("wsbar.confirm.start.go"),
      danger: false,
    });
    if (!ok) return;
    setStartQueued(true);
    if (!busy) void startWs();
  };

  // Start is immediate; Stop is confirmed — it docker-removes the container, so
  // running sessions drop to "stopped" (resumable) and opencode web / preview disconnect.
  // Reversible (Start recreates; data persists in the bind mount), so it's a caution,
  // not a red destructive action.
  const onToggle = async () => {
    if (!powerStops) {
      void startWs();
      return;
    }
    const ok = await askConfirm({
      title: tr("wsbar.stop_ws"),
      // Stopping a start that has not landed yet loses nothing — there are no sessions
      // to drop and no preview to disconnect — so it gets its own body rather than the
      // running one's warning about both.
      body: tr(running ? "wsbar.confirm.stop.body" : "wsbar.confirm.stop.starting_body"),
      confirmLabel: tr("wsbar.confirm.stop.go"),
      danger: false,
    });
    if (ok) void stopWs();
  };

  // Backend drift (CP-detected): the running container predates the deployed build,
  // so a stop→start would pick up the new backend. Shown only while genuinely
  // running — never mid-transition, where the restart is already happening. It is a
  // state, not an event: it clears itself once the workspace is back on the current
  // build, so there is no dismiss.
  const staleShown = wsStale && running && !busy;
  useEffect(() => {
    if (!staleShown) setStaleOpen(false);
  }, [staleShown]);
  // ecs-ec2: an administrator retired the machine this workspace sits on (#1473). Shown
  // whether running or stopped, hidden while a start (the move itself) runs.
  const slotMoveShown = wsSlotReplace && !busy;

  // Apply the backend update: stop→start, keeping repos and everything on disk
  // (NOT recreate). Sessions stop and are resumable, so name the count up front —
  // that's the whole cost of the action.
  const onRestart = async () => {
    setStaleOpen(false);
    const live = useSessionsStore.getState().sessions.filter((s) => s.alive).length;
    const ok = await askConfirm({
      title: tr("wsbar.confirm.restart.title"),
      body: live > 0 ? tr("wsbar.confirm.restart.body_live", { n: live }) : tr("wsbar.confirm.restart.body"),
      confirmLabel: tr("wsbar.confirm.restart.go"),
      danger: false,
    });
    if (ok) void restartWs();
  };

  // Enter in the port field does what a plain click on the primary button does: the default
  // target (subdomain, else browser pane, else lightweight preview — features/preview).
  const openDefaultPort = useDefaultPortOpen(Number(port.trim()), running, previewPath.trim() || "/");
  const closePortPops = () => {
    setPvOpen(false);
    setMoreOpen(false);
  };

  // Read the live Chromium attachments when a popover that shows them opens.
  // Deliberately not polled: the pane's own socket is the authoritative state,
  // and this list only has to be right at the moment the user looks at it.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    if (!running) {
      setAttachments((prev) => (prev.length ? [] : prev));
      return;
    }
    let cancelled = false;
    // No attachment can exist where the runtime has no browser features; asking would only
    // collect a browser_unavailable refusal.
    if (browserUnavailable) setAttachments((prev) => (prev.length ? [] : prev));
    else
      void listBrowserAttachments().then(
        (list) => !cancelled && setAttachments(list),
        () => !cancelled && setAttachments((prev) => (prev.length ? [] : prev)),
      );
    void usePreviewStore.getState().refresh({ force: true });
    void api("api/env/ws-settings").then(
      (res: any) => {
        if (cancelled) return;
        setPvHosts(
          res && !res.error && res.previewDomain
            ? {
                domain: res.previewDomain,
                urls: res.previewUrls || {},
                shareLinks: res.previewShareLinks || {},
                public: !!res.previewPublic,
                tenantShare: !!res.previewTenantShare,
              }
            : null,
        );
      },
      () => !cancelled && setPvHosts(null),
    );
    return () => {
      cancelled = true;
    };
  }, [pvOpen, moreOpen, running, browserUnavailable]);

  // Previews shared within this tenant (docs/log/81 §14.6). Not conditioned on running:
  // what appears here belongs to OTHER people's workspaces, so whether ours is stopped is
  // irrelevant.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    let cancelled = false;
    void api("api/preview/shared").then(
      (res: any) => !cancelled && setPvShared(res && !res.error ? res.items || [] : []),
      () => !cancelled && setPvShared([]),
    );
    return () => {
      cancelled = true;
    };
  }, [pvOpen, moreOpen]);

  // Close each popover on an outside click / Escape.
  useDismiss(pvRef, pvOpen, () => setPvOpen(false));
  useDismiss(staleRef, staleOpen, () => setStaleOpen(false));
  useDismiss(moreRef, moreOpen, () => setMoreOpen(false));
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

  const previewPop = (
    <>
      <div className="pv-section">
        <label className="pv-label">{tr("wsbar.preview.port_label")}</label>
        <div className="pv-row">
          <input
            className="preview-port"
            type="number"
            min="1"
            max="65535"
            placeholder={tr("wsbar.preview.port_ph")}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              openDefaultPort();
              closePortPops();
            }}
            title={tr("wsbar.preview.port_hint")}
          />
          <input
            className="preview-path"
            value={previewPath}
            onChange={(e) => setPreviewPath(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              openDefaultPort();
              closePortPops();
            }}
            aria-label={tr("browser.path")}
            title={tr("wsbar.preview.path_hint")}
          />
        </div>
        <PortOpen
          variant="buttons"
          port={Number(port.trim())}
          path={previewPath.trim() || "/"}
          running={!!running}
          onOpened={closePortPops}
        />
        <div className="pv-hint">
          {browserUnavailable
            ? tr("wsbar.preview.hint_unavailable", { runtime: browserUnavailable })
            : tr("wsbar.preview.hint")}
        </div>
      </div>
      {/* The issued preview subdomain (docs/log/81). No URL is shown while stopped: the
          slug is issued per start and expires on stop, so the link would only 404 — and a
          404 comes back as a report that the feature is broken. */}
      {pvHosts && Object.keys(pvHosts.urls).length > 0 && (
        <div className="pv-section">
          <label className="pv-label">
            {tr("wsbar.preview.hosts_label")}
            {pvHosts.public && <span className="pv-public"> {tr("wsbar.preview.public_badge")}</span>}
          </label>
          {Object.keys(pvHosts.urls)
            .sort((a, b) => Number(a) - Number(b))
            .map((p) => (
              <div className="pv-row pv-actions" key={p}>
                <Button
                  className="pv-host"
                  title={pvHosts.urls[p]}
                  onClick={() => {
                    window.open(pvHosts.urls[p], "_blank", "noopener");
                    setPvOpen(false);
                    setMoreOpen(false);
                  }}
                >
                  <Icon name="globe" /> :{p}
                </Button>
                <Button
                  variant="ghost"
                  title={tr("wsbar.preview.copy_url")}
                  onClick={() => void navigator.clipboard?.writeText(pvHosts.urls[p])}
                >
                  {tr("wsbar.preview.copy")}
                </Button>
                {/* Shown only while sharing: the link that survives restarts (docs/log/81
                    §14.6). The raw URL rots on every start, so this is the one to hand
                    to someone. */}
                {pvHosts.tenantShare && pvHosts.shareLinks[p] && (
                  <Button
                    variant="ghost"
                    title={tr("wsbar.preview.copy_share_url")}
                    onClick={() =>
                      void navigator.clipboard?.writeText(window.location.origin + pvHosts.shareLinks[p])
                    }
                  >
                    {tr("wsbar.preview.copy_share")}
                  </Button>
                )}
              </div>
            ))}
          <div className="pv-hint">{tr("wsbar.preview.hosts_hint")}</div>
        </div>
      )}
      {/* Unlisted ports the owner has opened for this start. They are the owner's alone and
          end with the listener, so they are listed apart from the issued URLs above. */}
      {tempPorts.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("portopen.temp_badge")}</label>
          <div className="pv-row pv-actions">
            {tempPorts.map((p) => (
              <PortOpen key={p} variant="pill" port={p} running={!!running} />
            ))}
          </div>
          <div className="pv-hint">{tr("portopen.temp_hint")}</div>
        </div>
      )}
      {/* Previews shared by other people in the same tenant (docs/log/81 §14.6). What opens
          is a stable link on the Console origin that resolves the current slug; carrying the
          raw URL around 404s the moment its owner restarts. */}
      {pvShared.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("wsbar.preview.shared_label")}</label>
          {pvShared.map((s) => (
            <div className="pv-row pv-actions pv-row-shared" key={s.ownerUserKey}>
              <span className="pv-shared-owner" title={s.ownerEmail || s.ownerUserKey}>
                {s.ownerEmail || s.ownerUserKey}
              </span>
              {s.running ? (
                s.ports.map((p) => (
                  <Button
                    className="pv-host"
                    key={p}
                    title={s.shareLinks[String(p)]}
                    onClick={() => {
                      window.open(window.location.origin + s.shareLinks[String(p)], "_blank", "noopener");
                      setPvOpen(false);
                      setMoreOpen(false);
                    }}
                  >
                    <Icon name="globe" /> :{p}
                  </Button>
                ))
              ) : (
                // Keep the row while stopped; removing it would read as "not shared".
                <span className="muted">{tr("wsbar.preview.shared_stopped")}</span>
              )}
            </div>
          ))}
          <div className="pv-hint">{tr("wsbar.preview.shared_hint")}</div>
        </div>
      )}
      {/* Way back to an existing Chromium an agent attached to (docs/log/53 §53.7). The
          intended entry is the action link in the mirror; this keeps a live attachment
          reachable after the conversation scrolls past it or the pane is closed. */}
      {attachments.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("wsbar.preview.attachments_label")}</label>
          {attachments.map((a) => (
            <div className="pv-row pv-actions" key={a.id}>
              <Button
                className="pv-attachment"
                onClick={() => {
                  setPvOpen(false);
                  setMoreOpen(false);
                  void openBrowserAttachment(a.id);
                }}
                title={a.url || a.title}
              >
                {a.title || tr("browser.attach.canvas")}
              </Button>
            </div>
          ))}
        </div>
      )}
    </>
  );

  // The pane buttons that fold into ⋯ on a narrow phone (wsBarFold.ts, useWsBarPhoneFold) are
  // rendered from these in both places, so the bar's copy and the popover's copy cannot drift.
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
      {/* "Restart required" — the backend moved on while this container kept running. Sits
          right of the state chip (and dots the power button next to it) because the
          fix IS the power toggle; the popover explains the cost and offers the
          stop→start in one click. Label folds away on a phone, tap target stays. */}
      {staleShown && (
        <div className="ws-stale" ref={staleRef}>
          <Button
            className="ws-stale-pill"
            onClick={() => setStaleOpen((o) => !o)}
            aria-expanded={staleOpen}
            title={tr("wsbar.stale.title")}
          >
            <Icon name="refresh" />
            <span className="lbl">{tr("wsbar.stale.badge")}</span>
          </Button>
          {staleOpen && (
            <div className="ws-stale-pop">
              <div className="ws-stale-txt">{tr("wsbar.stale.body")}</div>
              <div className="ws-stale-actions">
                <Button className="ws-stale-go" onClick={() => void onRestart()}>
                  {tr("wsbar.stale.restart")}
                </Button>
                <Button variant="ghost" onClick={() => setStaleOpen(false)}>
                  {tr("wsbar.stale.later")}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
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
      {/* Columns are a desktop layout: canSplitRight is false for the whole phone width, so
          the button could only ever sit there disabled — and the bar has no room to spare
          (an overflowing bar widens the page, see wsbar.css). Dropped rather than disabled;
          it returns with the width, since useIsMobile tracks the media query. */}
      {!isMobile && (
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
      )}
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
        <div className="ws-more" ref={moreRef}>
          <Button
            variant="ghost"
            className="ws-more-btn"
            title={tr("wsbar.more_title")}
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </Button>
          {moreOpen && (
            <div className="ws-more-pop">
              {/* The pane buttons the bar had no room for (the phone fold); a tap runs the action
                  and closes the popover. In bar order, so the popover reads like the bar did. */}
              {phoneFold > 0 && (
                <div
                  className="ws-more-actions"
                  onClick={(e) => {
                    // Closing unmounts the focused button; hand focus back to the trigger so a
                    // keyboard user is not dropped on <body>. Only when focus is still here: an
                    // action that moved it elsewhere (a pane) keeps it.
                    if (e.currentTarget.contains(document.activeElement)) {
                      moreRef.current?.querySelector<HTMLElement>(".ws-more-btn")?.focus();
                    }
                    setMoreOpen(false);
                  }}
                >
                  {phoneFold > 1 && splitDownBtn}
                  {phoneFold > 1 && closeAllBtn}
                  {homeBtn}
                  {overviewBtn}
                  {imagegenBtn}
                </div>
              )}
              {/* statsBlock is always a truthy Fragment (each chip hides itself by returning
                  null), so emptiness cannot be tested here; the padding of an empty block is
                  folded away by CSS :empty (wsbar.css). */}
              <div className="ws-more-stats">{statsBlock}</div>
              {previewPop}
            </div>
          )}
        </div>
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
          <div className="ws-more ws-more-desk" ref={moreRef}>
            <Button
              variant="ghost"
              className="ws-more-btn"
              title={tr("wsbar.more_title")}
              aria-label={tr("wsbar.more_title")}
              aria-expanded={moreOpen}
              onClick={() => setMoreOpen((o) => !o)}
            >
              <Icon name="ellipsis" />
            </Button>
            {moreOpen && foldMore && (
              <div className="ws-more-pop">
                {/* statsBlock without the usage chips: those keep their own +N group. */}
                <div className="ws-more-stats">
                  {graphs}
                  {graphs && <MachineDetailsLink onNavigate={() => setMoreOpen(false)} />}
                  <AwsProfilesChip passive />
                  <GcpProfilesChip passive />
                </div>
                {previewPop}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
