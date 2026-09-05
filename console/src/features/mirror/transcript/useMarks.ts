// transcript/useMarks — fetching, adding and removing marks (docs/log/69 / ADR 0050).
//
// How an anchor is decided lives in marks.ts (pure, no React, no I/O). This is the wiring the
// mirror and the shared view share; they differ only in the endpoint and in who the viewer is.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON } from "../../../core/api/client.ts";
import { markRootKey, type NewMark, type TranscriptMark } from "./marks.ts";

/** The wiring handed to the render layer. An absent capability renders no control at all
 *  (the rule in capabilities.ts). */
export interface TranscriptMarksWiring {
  /** Root key -> the marks on that root. A changed reference is what triggers a repaint. */
  byRoot: Map<string, TranscriptMark[]>;
  /** Everything, newest first, for the list strip. */
  all: TranscriptMark[];
  /** May the viewer add marks? false renders no selection pill (a read-only recipient). */
  canEdit: boolean;
  add: (m: NewMark) => void;
  remove: (id: string) => void;
  /** May this mark be removed? The owner may remove anyone's; a recipient only their own. */
  canRemove: (m: TranscriptMark) => boolean;
  /** Display name of the author; "" = the owner. The viewer's own marks read as "You". */
  authorLabel: (author: string | undefined) => string;
  /**
   * Per-author colour slot (0 = the owner). The colour itself is the axis a reader picks to
   * carry meaning, so authorship is shown by the underline instead (ADR 0050 decision 5).
   */
  authorSlot: (author: string | undefined) => number;
  /** Lookup by id, for the card shown when a mark is clicked. */
  find: (id: string) => TranscriptMark | undefined;
}

/** How many underline colours are assigned to authors (matches --mark-author-N in the CSS). */
export const MARK_AUTHOR_SLOTS = 6;

/**
 * Floor on the refetch interval. Marks are auxiliary — the only cost of being late is that
 * someone else's mark appears a little after the fact — so refetching every second like the
 * transcript would only add round trips to the owner's Workspace.
 */
const MARKS_REFRESH_MS = 15000;

// authorSlotsOf maps author -> slot number. The owner ("") is always 0; the rest are assigned
// from 1 in login-id order — assigning by arrival order swaps the colours on every poll.
function authorSlotsOf(list: TranscriptMark[]): Map<string, number> {
  const others = [...new Set(list.map((m) => m.author || "").filter(Boolean))].sort();
  const map = new Map<string, number>([["", 0]]);
  others.forEach((a, i) => map.set(a, 1 + (i % (MARK_AUTHOR_SLOTS - 1))));
  return map;
}

function byRootOf(list: TranscriptMark[]): Map<string, TranscriptMark[]> {
  const map = new Map<string, TranscriptMark[]>();
  for (const m of list) {
    const key = markRootKey(m.turn, m.part);
    const at = map.get(key);
    if (at) at.push(m);
    else map.set(key, [m]);
  }
  return map;
}

export interface MarksControllerOptions {
  /** `api/sessions/<name>/marks` or `api/shared-sessions/<id>/marks`; "" disables the feature. */
  path: string;
  /** May the viewer add marks? Always true for the owner; for a recipient only on a RW share. */
  canEdit: boolean;
  /** Is the viewer the owner? (They may remove anyone's mark.) */
  isOwner: boolean;
  /** The viewer's own login id; "" for the owner. */
  viewerId: string;
  /** Display name of the owner, so the shared view can name the other party. */
  ownerLabel: string;
  /** The translation of "You", injected so this module need not pull in i18n. */
  youLabel: string;
  /** Suspends fetching (the owner's Workspace is stopped, for instance). */
  paused?: boolean;
}

/**
 * Fetches, adds and removes marks. Add and remove update optimistically and roll back on
 * failure: marks are auxiliary, so network trouble must never stall reading the conversation.
 *
 * Do not add a second poll loop. The transcript's poll calls `reload()`, and the actual refetch
 * is throttled by MARKS_REFRESH_MS.
 */
export function useMarksController(opts: MarksControllerOptions): TranscriptMarksWiring & { reload: () => void } {
  const { path, canEdit, isOwner, viewerId, ownerLabel, youLabel, paused } = opts;
  const [byRoot, setByRoot] = useState<Map<string, TranscriptMark[]>>(() => new Map());
  const [all, setAll] = useState<TranscriptMark[]>([]);
  const [slots, setSlots] = useState<Map<string, number>>(() => new Map());
  // The current list lives in a ref: another add or remove can land mid round trip (optimistic
  // update -> response), and rebuilding from an array captured in a closure lets the later
  // response undo the earlier change.
  const listRef = useRef<TranscriptMark[]>([]);
  const pathRef = useRef(path);
  const lastFetch = useRef(0);

  const apply = useCallback((list: TranscriptMark[]) => {
    listRef.current = list;
    setByRoot(byRootOf(list));
    setSlots(authorSlotsOf(list));
    setAll([...list].sort((a, b) => (b.created_at || 0) - (a.created_at || 0)));
  }, []);

  // On a session switch, never show the previous session's marks, not even for a frame.
  if (pathRef.current !== path) {
    pathRef.current = path;
    lastFetch.current = 0; // right after moving to another session, do not let the throttle bite
    if (listRef.current.length) apply([]);
  }

  // Called off the transcript's own poll (once a second), so the throttling happens here. This
  // exists to avoid a second interval: with two, the transcript and the marks would be looking
  // at different moments in time (same reason as docs/log/68 decision 3).
  const reload = useCallback(() => {
    if (!path || paused) return;
    const now = Date.now();
    if (now - lastFetch.current < MARKS_REFRESH_MS) return;
    lastFetch.current = now;
    void api(path).then((d) => {
      if (pathRef.current !== path) return; // a previous session's response, arriving after the switch
      if (!d || d.error || !Array.isArray(d.marks)) return;
      apply(d.marks as TranscriptMark[]);
    });
  }, [path, paused, apply]);

  useEffect(() => {
    reload();
  }, [reload]);

  const add = useCallback(
    (m: NewMark) => {
      if (!path || !canEdit) return;
      // The caller mints the id. The Agent side is create-only, so a resend adds no duplicate
      // (ADR 0050 decision 4 — idempotent without keeping a ledger of side effects).
      const id = "mk_" + newMarkHex();
      const optimistic: TranscriptMark = { ...m, id, author: viewerId || undefined, created_at: Date.now() };
      apply([...listRef.current, optimistic]);
      void apiJSON(path, "POST", { ...m, id }).then((d) => {
        if (pathRef.current !== path) return;
        const rest = listRef.current.filter((x) => x.id !== id);
        // Drop one that did not stick; otherwise replace it with the stored shape (created_at
        // and the author the CP stamped).
        apply(!d || d.error || !d.mark ? rest : [...rest, d.mark as TranscriptMark]);
      });
    },
    [path, canEdit, viewerId, apply],
  );

  const remove = useCallback(
    (id: string) => {
      if (!path) return;
      const gone = listRef.current.find((x) => x.id === id);
      apply(listRef.current.filter((x) => x.id !== id));
      void api(path + "?id=" + encodeURIComponent(id), { method: "DELETE" }).then((d) => {
        if (pathRef.current !== path) return;
        // Do not leave a mark gone when the delete failed (someone else's mark = 403 lands here).
        if (d && d.error && gone && !listRef.current.some((x) => x.id === id)) apply([...listRef.current, gone]);
      });
    },
    [path, apply],
  );

  const canRemove = useCallback(
    (m: TranscriptMark) => (isOwner ? true : !!viewerId && m.author === viewerId),
    [isOwner, viewerId],
  );

  const authorLabel = useCallback(
    (author: string | undefined) => {
      const who = author || "";
      if (who === viewerId) return youLabel; // covers both "" seen by the owner and a recipient's own
      return who || ownerLabel;
    },
    [viewerId, youLabel, ownerLabel],
  );

  const authorSlot = useCallback((author: string | undefined) => slots.get(author || "") ?? 0, [slots]);
  const find = useCallback((id: string) => listRef.current.find((m) => m.id === id), []);

  return { byRoot, all, canEdit, add, remove, canRemove, authorLabel, authorSlot, find, reload };
}

function newMarkHex(): string {
  const b = new Uint8Array(4);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}
