import { useEffect, useMemo, useRef, useState } from "react";
import type {
  KeyboardEvent as RKeyboardEvent,
  PointerEvent as RPointerEvent,
  ReactNode,
} from "react";
import { IconButton } from "../../ui/Button.tsx";
import type { BrowserOutbound, BrowserSnapshot } from "./protocol.ts";
import { BrowserInputBridge, clipboardShortcut, heldButton, modifiersOf, mouseButton, remotePoint, wheelPixels } from "./protocol.ts";

export interface BrowserSurfaceController {
  mount(canvas: HTMLCanvasElement): void;
  unmount(canvas: HTMLCanvasElement): void;
  setVisible(visible: boolean): void;
  setViewport(width: number, height: number): void;
  sendInput(message: BrowserOutbound): void;
  /** Copy the remote page's current selection to the user's clipboard. */
  copySelection?(): Promise<boolean>;
}

interface BrowserSurfaceProps {
  controller: BrowserSurfaceController;
  snapshot: Pick<BrowserSnapshot, "title" | "width" | "height">;
  canvasLabel: string;
  inputLabel: string;
  inputEnabled?: boolean;
  children?: ReactNode;
}

/** Shared screencast canvas and restricted pointer/keyboard/IME input surface. */
export function BrowserSurface({
  controller,
  snapshot,
  canvasLabel,
  inputLabel,
  inputEnabled = true,
  children,
}: BrowserSurfaceProps) {
  const [inputAnchor, setInputAnchor] = useState({ x: 0, y: 0 });
  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const imeRef = useRef<HTMLInputElement>(null);
  const inputBridge = useMemo(
    () => new BrowserInputBridge((message) => controller.sendInput(message)),
    [controller],
  );

  // React registers `wheel` on the ROOT container with {passive: true} (measured
  // in react-dom 19), so preventDefault() inside an onWheel prop is a silent
  // no-op: the wheel reached the remote page AND scrolled the Console's own
  // container out from under the pane. The listener has to be a native one on
  // the canvas with {passive: false}. Kept in a ref so the listener registered
  // by the mount effect never has to be torn down and re-added on every render.
  const wheelRef = useRef<(event: WheelEvent) => void>(() => {});
  useEffect(() => {
    wheelRef.current = (event: WheelEvent) => {
      if (!inputEnabled) return;
      event.preventDefault();
      const rect = canvasRef.current?.getBoundingClientRect();
      const p = rect ? remotePoint(event, rect, snapshot.width, snapshot.height) : { x: 0, y: 0 };
      controller.sendInput({
        type: "wheel",
        ...p,
        ...wheelPixels(event.deltaX, event.deltaY, event.deltaMode, snapshot.height),
        modifiers: modifiersOf(event),
      });
    };
  });

  useEffect(() => {
    const canvas = canvasRef.current;
    const stage = stageRef.current;
    if (!canvas || !stage) return;
    controller.mount(canvas);
    const onWheel = (event: WheelEvent) => wheelRef.current(event);
    canvas.addEventListener("wheel", onWheel, { passive: false });
    let intersecting = true;
    const syncVisibility = () => {
      const rect = stage.getBoundingClientRect();
      controller.setVisible(
        intersecting && rect.width > 0 && rect.height > 0 && document.visibilityState !== "hidden",
      );
    };
    // Agent restarts screencast after a viewport change. Debounce divider drags
    // so both owned and attached Chromium use one settled resize message.
    let viewportTimer = 0;
    const resize = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect;
      if (box && box.width > 0 && box.height > 0) {
        window.clearTimeout(viewportTimer);
        viewportTimer = window.setTimeout(() => controller.setViewport(box.width, box.height), 100);
      }
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
    if (initialRect.width > 0 && initialRect.height > 0) {
      controller.setViewport(initialRect.width, initialRect.height);
    }
    syncVisibility();
    return () => {
      window.clearTimeout(viewportTimer);
      canvas.removeEventListener("wheel", onWheel);
      resize.disconnect();
      intersection.disconnect();
      document.removeEventListener("visibilitychange", syncVisibility);
      controller.unmount(canvas);
    };
  }, [controller]);

  const point = (event: {
    clientX: number;
    clientY: number;
    altKey: boolean;
    ctrlKey: boolean;
    metaKey: boolean;
    shiftKey: boolean;
  }) => {
    const rect = canvasRef.current?.getBoundingClientRect();
    return rect ? remotePoint(event, rect, snapshot.width, snapshot.height) : { x: 0, y: 0 };
  };

  const onPointer = (event: RPointerEvent<HTMLCanvasElement>, kind: "move" | "down" | "up") => {
    if (!inputEnabled) return;
    if (event.pointerType === "touch") event.preventDefault();
    const p = point(event);
    if (kind === "down") {
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
      // A move during a drag must carry the button that is DOWN; "none" reads as
      // a hover and Blink drops the drag (scrollbar thumb, selection, sliders).
      button: kind === "move" ? heldButton(event.buttons) : mouseButton(event.button),
      buttons: event.buttons,
      modifiers: modifiersOf(event),
      clickCount: kind === "move" ? 0 : Math.max(1, event.detail),
    });
  };

  const onKeyDown = (event: RKeyboardEvent<HTMLInputElement>) => {
    // Clipboard shortcuts are handled HERE, never forwarded: the remote Chromium
    // runs in the container, so its clipboard is not the user's. Ctrl/Cmd+C asks
    // the page for its selection and writes it to the user's clipboard; Ctrl+V
    // must fall through un-prevented so the hidden input receives the native
    // paste, which onPaste then forwards as text.
    const shortcut = clipboardShortcut(event.nativeEvent);
    if (shortcut === "paste") return;
    if (shortcut === "copy" && controller.copySelection) {
      event.preventDefault();
      void controller.copySelection();
      return;
    }
    if (!inputEnabled) return;
    inputBridge.keyDown(event.nativeEvent);
    if (!event.nativeEvent.isComposing) event.preventDefault();
  };

  return (
    <div className="browser-stage" ref={stageRef}>
      <canvas
        ref={canvasRef}
        className="browser-canvas"
        aria-label={snapshot.title || canvasLabel}
        aria-disabled={!inputEnabled}
        // Keys are delivered by the hidden IME input, and focus used to reach it
        // ONLY through a canvas pointerdown — so a user who opened the pane and
        // just started typing got nothing until they happened to click first.
        // The canvas is focusable and hands focus straight on.
        tabIndex={inputEnabled ? 0 : -1}
        onFocus={() => imeRef.current?.focus({ preventScroll: true })}
        onPointerMove={(event) => onPointer(event, "move")}
        onPointerDown={(event) => onPointer(event, "down")}
        onPointerUp={(event) => onPointer(event, "up")}
        onPointerCancel={(event) => onPointer(event, "up")}
        onContextMenu={(event) => event.preventDefault()}
      />
      <input
        ref={imeRef}
        className="browser-ime"
        style={{ left: inputAnchor.x, top: inputAnchor.y }}
        aria-label={inputLabel}
        disabled={!inputEnabled}
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        onKeyDown={onKeyDown}
        onKeyUp={(event) => {
          if (clipboardShortcut(event.nativeEvent)) return;
          if (inputEnabled) inputBridge.keyUp(event.nativeEvent);
        }}
        onPaste={(event) => {
          event.preventDefault();
          if (!inputEnabled) return;
          const text = event.clipboardData.getData("text/plain");
          if (text) controller.sendInput({ type: "text", text });
        }}
        onCompositionStart={() => inputEnabled && inputBridge.compositionStart()}
        onCompositionEnd={(event) => {
          if (inputEnabled) inputBridge.compositionEnd(event.data);
          event.currentTarget.value = "";
        }}
        onInput={(event) => {
          if (!inputEnabled || event.nativeEvent.isComposing) return;
          inputBridge.input(event.currentTarget.value);
          event.currentTarget.value = "";
        }}
      />
      {children}
    </div>
  );
}

interface BrowserConsoleDrawerProps {
  entries: BrowserSnapshot["console"];
  emptyLabel: string;
  copyLabel: string;
  closeLabel: string;
  title: string;
  onClose(): void;
}

export function BrowserConsoleDrawer({ entries, emptyLabel, copyLabel, closeLabel, title, onClose }: BrowserConsoleDrawerProps) {
  const important = [...entries].sort((a, b) => logRank(b.level) - logRank(a.level));
  return (
    <aside className="browser-console">
      <div className="browser-console-head">
        <strong>{title}</strong>
        <span>
          <IconButton
            icon="copy"
            label={copyLabel}
            disabled={entries.length === 0}
            onClick={() => void navigator.clipboard?.writeText(entries.map((entry) => `[${entry.level}] ${entry.text}`).join("\n"))}
          />
          <IconButton icon="close" label={closeLabel} onClick={onClose} />
        </span>
      </div>
      <div className="browser-console-body">
        {important.length === 0 ? <div className="browser-console-empty">{emptyLabel}</div> : important.map((entry, index) => (
          <div className={`browser-log browser-log-${entry.level}`} key={`${entry.ts}-${index}`}>
            <span>{entry.level}</span><pre>{entry.text}</pre>
          </div>
        ))}
      </div>
    </aside>
  );
}

function logRank(level: string): number {
  return level === "error" ? 3 : level === "warn" || level === "warning" ? 2 : 1;
}
