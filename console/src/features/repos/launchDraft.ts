// Draft of the first prompt in the "start work" dialog — kept per repository in
// localStorage. Merely closing the modal does not clear it: stepping away to check a
// location or a branch and coming back, or confirming something on another screen before
// adding to the text, is ordinary use, and losing what was typed each time is close to an
// accident. It is cleared only when a session actually launched, the one point at which
// the prompt has fully reached the new session (pushPromptHistory keeps the history
// separately).
//
// Device-local for the same reason as launchPrefs: half-written text is the content of the
// box open on THIS device, not something that should surface in another device's launch
// dialog.
import { useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { clearDraft, readDraft } from "../../lib/draft.ts";

const KEY = (repo: string): string | null => (repo ? "af.launch-prompt." + repo : null);

// Key for the attachment (pasted image) draft. Same per-repository scope and lifetime as
// the text, but it lives in IndexedDB (lib/attachDraft): image bytes in localStorage would
// use up the whole 5MB budget they share with settings and UI notes on their own.
export const launchAttachKey = (repo: string): string | null => (repo ? "af.launch-attach." + repo : null);

export function readLaunchPrompt(repo: string): string {
  return readDraft(KEY(repo));
}

export function clearLaunchPrompt(repo: string): void {
  clearDraft(KEY(repo));
}

// useLaunchPrompt is a useState<string> backed by localStorage (the same shape as lib/draft's
// useDraft). Two differences:
//   - a seed (the initial prompt pushed in by a handoff proposal, a memo send or a work item)
//     outranks the draft: the caller opened the box having decided "start from this text", so
//     an earlier half-written draft must not overwrite it.
//   - the returned clear() deletes the stored draft and stops any further write-back, so a
//     re-render between a successful launch and unmount cannot resurrect it.
export function useLaunchPrompt(
  repo: string,
  seed?: string,
): [string, Dispatch<SetStateAction<string>>, () => void] {
  const key = KEY(repo);
  const [prompt, setPrompt] = useState(() => seed ?? readDraft(key));
  const keyRef = useRef(key);
  const launchedRef = useRef(false);
  useEffect(() => {
    if (keyRef.current !== key) {
      // The repository changed while the dialog stayed open (another copy picked in the
      // Start hub). Do not write the previous repository's text under the new key; read
      // the draft belonging to that key instead.
      keyRef.current = key;
      launchedRef.current = false;
      setPrompt(readDraft(key));
      return;
    }
    if (launchedRef.current || !key) return;
    try {
      if (prompt) localStorage.setItem(key, prompt);
      else localStorage.removeItem(key);
    } catch {
      /* private mode / quota — the draft simply is not kept */
    }
  }, [prompt, key]);
  const clear = () => {
    launchedRef.current = true;
    clearDraft(key);
  };
  return [prompt, setPrompt, clear];
}
