// Opening 「モデルを追加」 (ADR 0072 follow-up). Its own module, like the sessions overview's:
// the admin panel imports it, and a module that reaches the layout store must not drag the
// whole pane tree into a test that only renders the panel.
import { useLayoutStore } from "../../../layout/store.ts";

export function openEngineAdd(engineKey: string, lora: boolean): void {
  useLayoutStore.getState().openTarget({ content: { kind: "engineAdd", engineKey, lora } });
}
