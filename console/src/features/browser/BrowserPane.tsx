import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent as RKeyboardEvent, PointerEvent as RPointerEvent, WheelEvent as RWheelEvent } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Button, IconButton } from "../../ui/Button.tsx";
import type { BrowserSnapshot } from "./protocol.ts";
import { BrowserInputBridge, modifiersOf, mouseButton, remotePoint } from "./protocol.ts";
import { ensureBrowser } from "./service.ts";
import { browserTarget, targetFromURL } from "./target.ts";

interface BrowserPaneProps {
  paneId: string;
  port: number;
  path: string;
}

export function BrowserPane({ paneId, port, path }: BrowserPaneProps) {
  const tr = useT();
  const setPaneTarget = useLayoutStore((state) => state.setPaneTarget);
  // Resolve on every render so a tenant-scope registry reset can replace a
  // same-named pane's disposed controller without persisting another identity.
  const controller = ensureBrowser(paneId, { port, path });
  const [snapshot, setSnapshot] = useState<BrowserSnapshot>(controller.snapshot);
  const [portDraft, setPortDraft] = useState(String(port));
  const [pathDraft, setPathDraft] = useState(path);
  const [targetError, setTargetError] = useState(false);
  const [consoleOpen, setConsoleOpen] = useState(false);
  const [inputAnchor, setInputAnchor] = useState({ x: 0, y: 0 });
  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const imeRef = useRef<HTMLInputElement>(null);
  const inputBridge = useMemo(() => new BrowserInputBridge((message) => controller.sendInput(message)), [controller]);

  useEffect(() => {
    const target = browserTarget(port, path);
    if (target && (controller.target.port !== target.port || controller.target.path !== target.path)) controller.changeTarget(target);
    setPortDraft(String(port));
    setPathDraft(path);
  }, [controller, port, path]);

  useEffect(() => controller.subscribe((next) => {
    setSnapshot(next);
    const target = targetFromURL(next.url);
    if (!target || (target.port === controller.target.port && target.path === controller.target.path)) return;
    controller.adoptTarget(target);
    setPaneTarget(paneId, { content: { kind: "browser", ...target } });
  }), [controller, paneId, setPaneTarget]);

  useEffect(() => {
    const canvas = canvasRef.current;
    const stage = stageRef.current;
    if (!canvas || !stage) return;
    controller.mount(canvas);
    let intersecting = true;
    const syncVisibility = () => {
      const rect = stage.getBoundingClientRect();
      controller.setVisible(intersecting && rect.width > 0 && rect.height > 0 && document.visibilityState !== "hidden");
    };
    const resize = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect;
      if (box && box.width > 0 && box.height > 0) controller.setViewport(box.width, box.height);
      syncVisibility();
    });
    resize.observe(stage);
    const intersection = new IntersectionObserver((entries) => {
      intersecting = entries[0]?.isIntersecting ?? false;
      syncVisibility();
    });
    intersection.observe(stage);
    document.addEventListener("visibilitychange", syncVisibility);
    const initialRect = stage.getBoundingClientRect();
    if (initialRect.width > 0 && initialRect.height > 0) controller.setViewport(initialRect.width, initialRect.height);
    syncVisibility();
    return () => {
      resize.disconnect();
      intersection.disconnect();
      document.removeEventListener("visibilitychange", syncVisibility);
      controller.unmount(canvas);
    };
  }, [controller]);

  const submitTarget = (event: FormEvent) => {
    event.preventDefault();
    const target = browserTarget(Number(portDraft), pathDraft);
    if (!target) {
      setTargetError(true);
      return;
    }
    setTargetError(false);
    controller.changeTarget(target);
    setPaneTarget(paneId, { content: { kind: "browser", ...target } });
  };

  const point = (event: { clientX: number; clientY: number; altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }) => {
    const rect = canvasRef.current?.getBoundingClientRect();
    return rect ? remotePoint(event, rect, snapshot.width, snapshot.height) : { x: 0, y: 0 };
  };

  const onPointer = (event: RPointerEvent<HTMLCanvasElement>, kind: "move" | "down" | "up") => {
    if (event.pointerType === "touch") event.preventDefault();
    const p = point(event);
    if (kind === "down") {
      // The canvas is not focusable, so a mousedown on it makes the browser clear
      // focus to <body> — which would blur the hidden IME input we focus just
      // below, swallowing every subsequent keystroke (plain ASCII typing never
      // reaches onKeyDown). Suppressing the pointerdown default keeps that focus.
      event.preventDefault();
      event.currentTarget.setPointerCapture(event.pointerId);
      setInputAnchor({ x: event.nativeEvent.offsetX, y: event.nativeEvent.offsetY });
      imeRef.current?.focus({ preventScroll: true });
    } else if (kind === "up" && event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    controller.sendInput({
      type: "mouse",
      event: kind,
      ...p,
      button: kind === "move" ? "none" : mouseButton(event.button),
      buttons: event.buttons,
      modifiers: modifiersOf(event),
      clickCount: kind === "move" ? 0 : Math.max(1, event.detail),
    });
  };

  const onWheel = (event: RWheelEvent<HTMLCanvasElement>) => {
    event.preventDefault();
    controller.sendInput({
      type: "wheel",
      ...point(event),
      deltaX: event.deltaX,
      deltaY: event.deltaY,
      modifiers: modifiersOf(event),
    });
  };

  const onKeyDown = (event: RKeyboardEvent<HTMLInputElement>) => {
    inputBridge.keyDown(event.nativeEvent);
    if (!event.nativeEvent.isComposing) event.preventDefault();
  };
  const onKeyUp = (event: RKeyboardEvent<HTMLInputElement>) => inputBridge.keyUp(event.nativeEvent);

  const status = browserStatus(snapshot, tr);
  const importantLogs = [...snapshot.console].sort((a, b) => logRank(b.level) - logRank(a.level));

  return (
    <div className="browser-pane">
      <form className="browser-toolbar" onSubmit={submitTarget}>
        <IconButton icon="arrow-left" className="browser-nav" label={tr("browser.back")} disabled={!snapshot.canBack} onClick={() => controller.history("back")} />
        <IconButton icon="arrow-right" className="browser-nav" label={tr("browser.forward")} disabled={!snapshot.canForward} onClick={() => controller.history("forward")} />
        <IconButton icon="refresh" label={tr("browser.reload")} onClick={() => controller.reload()} />
        <span className="browser-host">127.0.0.1:</span>
        <input
          className="browser-port"
          aria-label={tr("browser.port")}
          inputMode="numeric"
          value={portDraft}
          onChange={(event) => setPortDraft(event.target.value)}
        />
        <input
          className={"browser-path" + (targetError ? " invalid" : "")}
          aria-label={tr("browser.path")}
          aria-invalid={targetError}
          value={pathDraft}
          onChange={(event) => setPathDraft(event.target.value)}
        />
        <Button small variant="ghost" icon="terminal" className="browser-console-toggle" onClick={() => setConsoleOpen((open) => !open)}>
          {tr("browser.console")}{snapshot.console.length > 0 && <span className="browser-log-badge">{snapshot.console.length}</span>}
        </Button>
        <IconButton icon="debug-restart" label={tr("browser.reconnect")} onClick={() => void controller.reconnect()} />
      </form>
      <div className="browser-stage" ref={stageRef}>
        <canvas
          ref={canvasRef}
          className="browser-canvas"
          aria-label={snapshot.title || tr("browser.canvas")}
          onPointerMove={(event) => onPointer(event, "move")}
          onPointerDown={(event) => onPointer(event, "down")}
          onPointerUp={(event) => onPointer(event, "up")}
          onPointerCancel={(event) => onPointer(event, "up")}
          onWheel={onWheel}
          onContextMenu={(event) => event.preventDefault()}
        />
        <input
          ref={imeRef}
          className="browser-ime"
          style={{ left: inputAnchor.x, top: inputAnchor.y }}
          aria-label={tr("browser.remote_input")}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onKeyDown={onKeyDown}
          onKeyUp={onKeyUp}
          onCompositionStart={() => inputBridge.compositionStart()}
          onCompositionEnd={(event) => {
            inputBridge.compositionEnd(event.data);
            event.currentTarget.value = "";
          }}
          onInput={(event) => {
            if (event.nativeEvent.isComposing) return;
            const text = event.currentTarget.value;
            inputBridge.input(text);
            event.currentTarget.value = "";
          }}
        />
        {status && (
          <div className={"browser-state browser-state-" + snapshot.state}>
            <span>{status}</span>
            {(snapshot.state === "crashed" || snapshot.state === "disconnected" || snapshot.state === "target-unreachable") && (
              <Button small icon="debug-restart" onClick={() => void controller.reconnect()}>{tr("browser.reconnect")}</Button>
            )}
          </div>
        )}
        {consoleOpen && (
          <aside className="browser-console">
            <div className="browser-console-head">
              <strong>{tr("browser.console")}</strong>
              <span>
                <IconButton
                  icon="copy"
                  label={tr("browser.copy_console")}
                  disabled={snapshot.console.length === 0}
                  onClick={() => void navigator.clipboard?.writeText(snapshot.console.map((entry) => `[${entry.level}] ${entry.text}`).join("\n"))}
                />
                <IconButton icon="close" label={tr("common.close")} onClick={() => setConsoleOpen(false)} />
              </span>
            </div>
            <div className="browser-console-body">
              {importantLogs.length === 0 ? <div className="browser-console-empty">{tr("browser.console_empty")}</div> : importantLogs.map((entry, index) => (
                <div className={"browser-log browser-log-" + entry.level} key={`${entry.ts}-${index}`}>
                  <span>{entry.level}</span><pre>{entry.text}</pre>
                </div>
              ))}
            </div>
          </aside>
        )}
      </div>
    </div>
  );
}

function logRank(level: string): number {
  return level === "error" ? 3 : level === "warn" || level === "warning" ? 2 : 1;
}

function browserStatus(snapshot: BrowserSnapshot, tr: ReturnType<typeof useT>): string {
  if (snapshot.errorCode) {
    const known: Record<string, Parameters<typeof tr>[0]> = {
      workspace_stopped: "browser.workspace_stopped",
      workspace_starting: "browser.workspace_starting",
      browser_page_limit: "browser.page_limit",
      browser_protocol_mismatch: "browser.protocol_mismatch",
      browser_installing: "browser.installing",
    };
    const key = known[snapshot.errorCode];
    return key ? tr(key) : snapshot.errorMessage || tr("browser.disconnected");
  }
  if (snapshot.state === "loading") return tr("browser.loading");
  if (snapshot.state === "target-unreachable") return tr("browser.target_unreachable");
  if (snapshot.state === "crashed") return tr("browser.crashed");
  if (snapshot.state === "disconnected") return tr("browser.disconnected");
  return "";
}
