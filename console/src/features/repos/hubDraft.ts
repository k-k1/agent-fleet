// The Start hub's one draft (ADR 0142 decision 5): the prompt and its attachments, shown in
// every section of the hub and carried into the launch it ends in (a space, home, or a
// repository's start-work dialog). It lives for one opening of the hub: every way in starts it
// over from the launch seed (a memo being sent or restored), as the home dialog it replaces
// as the place for repo-less text never kept a draft either.
import { useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { makeAttachment, useAttachDraft } from "../../lib/attachDraft.ts";
import type { AttachDraft } from "../../lib/attachDraft.ts";
import { fetchMemoAttachments } from "../memo/launchStash.ts";
import type { MemoAttachment } from "../../types/memo.ts";

/** A draft the launch form edits in place of its own per-repository one. */
export interface SharedDraft {
  prompt: string;
  setPrompt: Dispatch<SetStateAction<string>>;
  attach: AttachDraft;
  /** A seed's files are still being fetched: nothing may launch or be saved without them. */
  loading: boolean;
  /** Seed files that could not be fetched (a memo whose files are gone from the server). */
  missing: number;
}

/** Whether the draft holds anything a launch would carry. */
export const draftHasContent = (d: SharedDraft): boolean => !!d.prompt.trim() || d.attach.items.length > 0 || d.loading;

export interface HubDraft extends SharedDraft {
  /** Start over from a seed (an empty one for a plain opening of the hub). */
  reset: (seed: { prompt: string; attachments: MemoAttachment[] }) => void;
}

export function useHubDraft(): HubDraft {
  const [prompt, setPrompt] = useState("");
  // No key: the hub's files stay in memory, for the same one-opening life as the text.
  const attach = useAttachDraft(null);
  const [loading, setLoading] = useState(false);
  const [missing, setMissing] = useState(0);
  // A reset made while an earlier seed's files were still on the wire must not get them.
  const gen = useRef(0);
  const reset = ({ prompt: text, attachments }: { prompt: string; attachments: MemoAttachment[] }) => {
    const mine = ++gen.current;
    setPrompt(text);
    attach.clear();
    setMissing(0);
    setLoading(attachments.length > 0);
    if (!attachments.length) return;
    void fetchMemoAttachments(attachments).then((files) => {
      if (mine !== gen.current) return;
      attach.add(files.map((f) => makeAttachment(f)));
      setMissing(attachments.length - files.length);
      setLoading(false);
    });
  };
  return { prompt, setPrompt, attach, loading, missing, reset };
}
