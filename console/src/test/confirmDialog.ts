// Drives the app's <ConfirmProvider> dialog from a dom test. The dialog portals to <body>, so
// it is queried from the document rather than from the test's host element.
import { act } from "react";

export const confirmDialog = (): HTMLElement | null => document.querySelector<HTMLElement>(".ui-confirm");

/** answerConfirm clicks the dialog's confirm (true) or cancel (false) button. Throws when no
 *  dialog is open, so a test cannot pass by never having asked. */
export async function answerConfirm(ok: boolean): Promise<void> {
  const buttons = confirmDialog()?.querySelectorAll<HTMLButtonElement>(".ui-confirm-actions button");
  if (!buttons || buttons.length !== 2) throw new Error("no confirm dialog is open");
  await act(async () => buttons[ok ? 1 : 0].click());
}
