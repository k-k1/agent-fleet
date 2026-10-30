// PortOpen — the one control for opening a port the workspace listens on, used by a session
// row's port chips and the workspace bar's port field. It offers the three ways a port can be
// looked at (portTargets.ts) and makes the default one a plain click:
//
//   chip     `:5173`  click = default target, Ctrl/⌘/middle-click = browser pane in a new layout
//            tab (as before), right-click / long-press / "⋯" = the menu with all three targets,
//            Copy URL and "Expose this port…"
//   buttons  the workspace bar: the default target as the primary button, the others beside it
//
// Opening a window must happen inside the click handler with no await before it (popup
// blockers), which is why the subdomain entry is a plain GET navigation the CP resolves.
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { previewURL } from "../../core/api/client.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useBrowserUnavailable } from "../browser/availability.ts";
import { browserTarget } from "../browser/target.ts";
import { useSettingsUI } from "../settings/store.ts";
import { copyableURL, portTargets, subdomainHost, subdomainOpenURL, type PortTarget } from "./portTargets.ts";
import { currentPreviewInfo, useCurrentPreviewInfo, usePreviewStore } from "./previewState.ts";

/** What the shared state says about this port, kept fresh while a control is on screen. */
export function usePortState(port: number, running: boolean) {
  const info = useCurrentPreviewInfo();
  const browserUnavailable = useBrowserUnavailable();
  // Read when nothing valid for this tenant is held (first control on screen, tenant switch);
  // restarts are handled once, by the workspace-state subscription in previewState.ts.
  useEffect(() => {
    if (running && !info) void usePreviewStore.getState().refresh();
  }, [running, info]);
  return { info, browserUnavailable, targets: portTargets({ port, running, browserUnavailable, info }) };
}

/** Open `port` in one target. `inNew` puts a browser pane in a new layout tab. Reads the stores
 *  at call time, so a Markdown link (no hook, built outside React) opens exactly as the chip. */
export function openPort(target: PortTarget, port: number, path: string, inNew = false) {
  const info = currentPreviewInfo();
  if (target === "subdomain" && info) {
    window.open(subdomainOpenURL(info, port, path), "_blank", "noopener");
  } else if (target === "pane") {
    const bt = browserTarget(port, path);
    if (!bt) return;
    const t = { content: { kind: "browser" as const, ...bt } };
    const layout = useLayoutStore.getState();
    if (inNew) layout.openTargetInNew(t, true);
    else layout.openTarget(t);
  } else {
    window.open(previewURL(port, path), "_blank", "noopener");
  }
}

function useOpenPort(port: number, path: string) {
  // Subscribed so a change of the held info re-renders the control that depends on it.
  useCurrentPreviewInfo();
  return (target: PortTarget, inNew = false) => openPort(target, port, path, inNew);
}

/** What Enter in a port field does: the default target, if any. */
export function useDefaultPortOpen(port: number, running: boolean, path = "/"): () => void {
  const { targets } = usePortState(port, running);
  const open = useOpenPort(port, path);
  return () => {
    if (targets.def) open(targets.def);
  };
}

const TARGET_ORDER: PortTarget[] = ["subdomain", "pane", "light"];

export function PortOpen({
  port,
  running,
  variant,
  path = "/",
  onOpened,
}: {
  port: number;
  running: boolean;
  variant: "chip" | "buttons" | "pill";
  path?: string;
  onOpened?: () => void;
}) {
  const tr = useT();
  const { info, targets } = usePortState(port, running);
  const open = useOpenPort(port, path);
  const go = (t: PortTarget, inNew = false) => {
    open(t, inNew);
    onOpened?.();
  };
  if (variant === "pill") {
    // A plain ui/Button for popovers, where every button has to be one.
    return (
      <Button
        className="pv-host"
        disabled={!targets.def}
        title={targets.def ? targetTitle(tr, targets.def, port, info) : undefined}
        onClick={() => targets.def && go(targets.def)}
      >
        <Icon name="globe" /> :{port}
      </Button>
    );
  }
  return variant === "chip" ? (
    <PortChip port={port} info={info} targets={targets} go={go} />
  ) : (
    <PortButtons port={port} info={info} targets={targets} go={go} />
  );
}

type Targets = ReturnType<typeof portTargets>;
type Info = ReturnType<typeof usePortState>["info"];
export interface Inner {
  port: number;
  info: Info;
  targets: Targets;
  go: (t: PortTarget, inNew?: boolean) => void;
}

function targetTitle(tr: ReturnType<typeof useT>, t: PortTarget, port: number, info: Info): string {
  if (t === "subdomain") {
    // The host the click really lands on: the issued URL for a listed port, the same slug
    // and domain with this port for an unlisted one (which the owner-only entry opens).
    const host = info ? subdomainHost(info, port) : "";
    return tr("portopen.title_subdomain", { port, url: host });
  }
  return tr(t === "pane" ? "portopen.title_pane" : "portopen.title_light", { port });
}

function PortChip({ port, info, targets, go }: Inner) {
  const tr = useT();
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const wrap = useRef<HTMLSpanElement>(null);
  const press = useRef<number | undefined>(undefined);
  // A long-press that opened the menu must not also "click" when the finger lifts.
  const longPressed = useRef(false);
  useEffect(() => () => window.clearTimeout(press.current), []);
  // The pane stays one gesture away: Ctrl/⌘/middle-click. Where it is unavailable the same
  // gesture opens the default, as the chip always opened something where it could.
  const inNewTarget: PortTarget = targets.state.pane === "" ? "pane" : (targets.def ?? "light");
  const click = (e: React.MouseEvent) => {
    if (longPressed.current) {
      longPressed.current = false;
      e.preventDefault();
      return;
    }
    if (!targets.def) return;
    if (e.ctrlKey || e.metaKey) go(inNewTarget, true);
    else go(targets.def);
  };
  const openMenu = (el: HTMLElement | null, at?: { x: number; y: number }) => {
    const r = el?.getBoundingClientRect();
    setMenu(at ?? { x: r ? r.left : 0, y: r ? r.bottom : 0 });
  };
  return (
    <span className="sess-port-wrap" ref={wrap}>
      <button
        type="button"
        className={"sess-port" + (targets.badge ? " port-" + targets.badge : "")}
        title={targets.def ? targetTitle(tr, targets.def, port, info) : tr("portopen.none")}
        onClick={click}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => {
          if (e.button !== 1) return;
          e.preventDefault();
          if (targets.def) go(inNewTarget, true);
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          openMenu(null, { x: e.clientX, y: e.clientY });
        }}
        onTouchStart={() => {
          window.clearTimeout(press.current);
          longPressed.current = false;
          press.current = window.setTimeout(() => {
            longPressed.current = true;
            openMenu(wrap.current);
          }, 500);
        }}
        onTouchEnd={() => window.clearTimeout(press.current)}
        onTouchMove={() => window.clearTimeout(press.current)}
        onTouchCancel={() => {
          window.clearTimeout(press.current);
          longPressed.current = false;
        }}
      >
        :{port}
      </button>
      <button
        type="button"
        className="sess-port-menu"
        aria-label={tr("portopen.menu", { port })}
        aria-haspopup="menu"
        title={tr("portopen.menu", { port })}
        onClick={() => openMenu(wrap.current)}
      >
        ⋯
      </button>
      {menu && <PortMenu {...{ port, info, targets, go }} at={menu} anchor={wrap} onClose={() => setMenu(null)} />}
    </span>
  );
}

/** The menu of one port: every target, Copy URL, Expose / preview settings. `anchor` is the
 *  element a press on which is not "outside" (the chip); a menu opened from a link has none. */
export function PortMenu({
  port,
  info,
  targets,
  go,
  at,
  anchor,
  onClose,
}: Inner & { at: { x: number; y: number }; anchor?: React.RefObject<HTMLElement | null>; onClose: () => void }) {
  const tr = useT();
  const ref = useRef<HTMLDivElement>(null);
  const openSettings = useSettingsUI((s) => s.openSettings);
  useDismiss(anchor ? [ref, anchor] : ref, true, onClose);
  const pick = (t: PortTarget) => {
    go(t);
    onClose();
  };
  // Clamp into the viewport: the rail is at the screen edge.
  const left = Math.max(4, Math.min(at.x, window.innerWidth - 270));
  const top = Math.max(4, Math.min(at.y, window.innerHeight - 260));
  const domain = !!info?.domain;
  return createPortal(
    <div ref={ref} className="port-menu" role="menu" style={{ left, top }}>
      {targets.badge !== "" && <div className="port-menu-badge">{tr(`portopen.badge.${targets.badge}`)}</div>}
      {TARGET_ORDER.map((t) => {
        const why = targets.state[t];
        return (
          <button
            key={t}
            type="button"
            role="menuitem"
            className={"port-menu-item" + (targets.def === t ? " is-default" : "")}
            disabled={!!why}
            title={why ? tr(`portopen.unavailable.${why}`) : undefined}
            onClick={() => pick(t)}
          >
            <span className="port-menu-name">
              {tr(`portopen.target.${t}`)}
              {targets.def === t && <span className="port-menu-def"> {tr("portopen.default")}</span>}
            </span>
            <span className="port-menu-hint">{why ? tr(`portopen.unavailable.${why}`) : tr(`portopen.hint.${t}`)}</span>
          </button>
        );
      })}
      <button
        type="button"
        role="menuitem"
        className="port-menu-item"
        onClick={() => {
          void navigator.clipboard?.writeText(copyableURL(info, port));
          onClose();
        }}
      >
        <span className="port-menu-name">{tr("portopen.copy")}</span>
      </button>
      {domain && (
        <button
          type="button"
          role="menuitem"
          className="port-menu-item"
          onClick={() => {
            if (targets.listed) openSettings("preview");
            else openSettings("preview", { addPort: port });
            onClose();
          }}
        >
          <span className="port-menu-name">
            {targets.listed ? tr("portopen.settings") : tr("portopen.expose")}
          </span>
        </button>
      )}
    </div>,
    document.body,
  );
}

/** Workspace bar form: the default target as the primary button, the others beside it. */
function PortButtons({ port, info, targets, go }: Inner) {
  const tr = useT();
  const browserUnavailable = useBrowserUnavailable();
  // The runtime's own wording (it names the runtime) for the pane, the generic one otherwise.
  const why = (t: PortTarget) =>
    targets.state[t] === "no_browser"
      ? tr("browser.unavailable.short", { runtime: browserUnavailable })
      : targets.state[t]
        ? tr(`portopen.unavailable.${targets.state[t] as Exclude<Targets["state"][PortTarget], "">}`)
        : undefined;
  const openSettings = useSettingsUI((s) => s.openSettings);
  // With no default the placeholder below stands in for "light"; listing it again doubles the button.
  const rest = TARGET_ORDER.filter((t) => t !== (targets.def || "light"));
  const label = (t: PortTarget) =>
    tr(t === "subdomain" ? "portopen.btn.subdomain" : t === "pane" ? "wsbar.preview.open_pane" : "wsbar.preview.open_light");
  return (
    <>
      <div className="pv-row pv-actions">
        {targets.def ? (
          <Button
            variant="primary"
            onClick={() => go(targets.def!)}
            title={targets.def === "subdomain" ? targetTitle(tr, "subdomain", port, info) : undefined}
          >
            {label(targets.def)}
          </Button>
        ) : (
          <Button disabled>{label("light")}</Button>
        )}
        {rest.map((t) => (
          <Button
            key={t}
            onClick={() => go(t)}
            disabled={!!targets.state[t]}
            title={why(t)}
          >
            {label(t)}
          </Button>
        ))}
      </div>
      {targets.valid && targets.badge === "only_you" && <div className="pv-hint">{tr("portopen.badge.only_you")}</div>}
      {info?.domain && targets.valid && (
        <div className="pv-hint">
          <Button
            variant="ghost"
            small
            onClick={() => (targets.listed ? openSettings("preview") : openSettings("preview", { addPort: port }))}
          >
            {targets.listed ? tr("portopen.settings") : tr("portopen.expose")}
          </Button>
        </div>
      )}
    </>
  );
}
