// Clipboard key decisions for the terminal, kept free of xterm and the DOM so the rules are
// unit-testable (jsdom has no real selection or clipboard).
//
// Plain Ctrl+C / Ctrl+V are the contested keys: Ctrl+C is SIGINT and must stay SIGINT whenever
// nothing is selected, or a running command can no longer be interrupted.
export type ClipAction = "copy" | "copyClear" | "paste";

export interface ClipKeyEvent {
  type: string;
  code: string;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  isComposing?: boolean;
  keyCode?: number;
}

// clipAction returns what a keydown should do to the clipboard, or null to leave the key to
// the PTY. ctrlCV is the "Ctrl+C copies a selection / Ctrl+V pastes" setting; the other
// bindings (Ctrl/Cmd+Shift+C/V, Ctrl+Insert, Shift+Insert, Cmd+C/V) work regardless.
// copiedAlready: the selection is already on the clipboard (copy-on-select), so Ctrl+C is an
// interrupt, not a second copy.
// "copyClear" copies and drops the selection, so the next Ctrl+C is an interrupt again.
export function clipAction(e: ClipKeyEvent, hasSelection: boolean, ctrlCV: boolean, copiedAlready = false): ClipAction | null {
  if (e.type !== "keydown") return null;
  // An IME owns the key while composing (keyCode 229); swallowing it breaks CJK input.
  if (e.isComposing || e.keyCode === 229) return null;
  const mod = e.ctrlKey || e.metaKey;
  if (mod && e.shiftKey && e.code === "KeyC") return "copy";
  if (mod && e.shiftKey && e.code === "KeyV") return "paste";
  if (e.ctrlKey && !e.shiftKey && e.code === "Insert") return "copy";
  if (e.shiftKey && !e.ctrlKey && e.code === "Insert") return "paste";
  if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyC" && hasSelection) return "copy";
  if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyV") return "paste";
  if (ctrlCV && e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey) {
    if (e.code === "KeyC" && hasSelection && !copiedAlready) return "copyClear";
    if (e.code === "KeyV") return "paste";
  }
  return null;
}
