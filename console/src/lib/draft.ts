// Composer-draft persistence (localStorage) shared by the mirror composer, the
// assistant-chat composer, and the memo-queue input. A draft is a plain string under a
// stable key; every edit is written through, so a reload / browser crash / pane unmount
// keeps what the user was typing. All accessors swallow storage errors (private mode,
// quota) — the draft just won't persist.
import { useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";

// readDraft loads a persisted draft ("" when none / unavailable).
export function readDraft(key: string | null): string {
  if (!key) return "";
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

export function clearDraft(key: string | null): void {
  if (!key) return;
  try {
    localStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

// writeDraft seeds a draft for a session the user is not looking at yet — the branch
// flow (docs/log/55) puts the branched-from prompt into the NEW session's composer, so the
// pane opens with it already typed and the user can edit or resend in one move. It never
// clobbers an existing draft: a session that already has unsent text keeps it.
export function writeDraft(key: string | null, text: string): void {
  if (!key || !text) return;
  try {
    if (localStorage.getItem(key)) return;
    localStorage.setItem(key, text);
  } catch {
    /* ignore */
  }
}

// moveDraft re-keys a stored draft (e.g. an assistant-chat draft pane promoted to its
// real conversation id) so a key change under a mounted useDraft doesn't lose the text.
export function moveDraft(from: string | null, to: string | null): void {
  const text = readDraft(from);
  clearDraft(from);
  if (!to || !text) return;
  try {
    localStorage.setItem(to, text);
  } catch {
    /* ignore */
  }
}

// useDraft is useState<string> backed by localStorage: initialized from the key, saved
// on every edit (removed when emptied), and — when the key changes under a mounted
// component (session/conversation switch) — reloaded from the new key instead of
// clobbering it with the old key's text. key=null disables persistence and clears.
export function useDraft(key: string | null): [string, Dispatch<SetStateAction<string>>] {
  const [draft, setDraft] = useState(() => readDraft(key));
  const keyRef = useRef<string | null>(key);
  useEffect(() => {
    if (keyRef.current !== key) {
      // Key switched under a mounted view — load the new key's draft instead of saving
      // the old one here.
      keyRef.current = key;
      setDraft(readDraft(key));
      return;
    }
    if (!key) return;
    try {
      if (draft) localStorage.setItem(key, draft);
      else localStorage.removeItem(key);
    } catch {
      /* storage unavailable (private mode) — draft just won't persist */
    }
  }, [draft, key]);
  return [draft, setDraft];
}
