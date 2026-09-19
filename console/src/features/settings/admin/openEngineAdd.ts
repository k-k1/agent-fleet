// Opening 「モデルを追加」 (ADR 0072 follow-up). Its own module, like the sessions overview's:
// the admin panel imports it, and a module that reaches the layout store must not drag the
// whole pane tree into a test that only renders the panel.
import { useLayoutStore } from "../../../layout/store.ts";

export function openEngineAdd(
  engineKey: string,
  lora: boolean,
  view?: "search" | "registered",
): void {
  useLayoutStore.getState().openTarget({
    content: { kind: "engineAdd", engineKey, lora, ...(view ? { view } : {}) },
  });
}

/** Record which face the catalogue is on, so a browser reload comes back to it.
 *
 * 🔴 `engineKey` and `lora` are the pane's OWN, passed straight back: `sameTarget` identifies an
 * engineAdd pane by exactly those two (ops.ts), so writing the role tab or model⇄lora in here
 * would change the pane's identity and the next `openEngineAdd` would open a second catalogue
 * beside it. The face is not part of that identity, which is why it is the one thing written. */
export function setEngineAddView(
  paneId: string,
  engineKey: string,
  lora: boolean,
  view: "search" | "registered",
): void {
  useLayoutStore.getState().setPaneTarget(paneId, {
    content: { kind: "engineAdd", engineKey, lora, view },
  });
}
