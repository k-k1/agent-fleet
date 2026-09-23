// useStudio — the studio pane's live copy of one Agent studio (ADR 0100 decisions 2, 3, 9).
//
// The studio is the truth of the draft; the form is a local copy the member types into. Three
// writers meet here, and the rules that keep them apart are:
//
//   - The member's edits go out as ONE merge patch 500 ms after the last keystroke, with the
//     `updated_at` last read as If-Match. A write that lost a race (the agent's set_image_draft
//     landed in the same half second) is refused, and the pane re-reads rather than overwriting.
//   - The agent's edits arrive on a 2 s poll, only while a session is bound (nobody else edits a
//     studio without one) and only while the tab is shown. A poll never overwrites the form while
//     the member's own edit is unsent.
//   - A press flushes the pending edit first, so what runs is what is on screen.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { MirrorSignal } from "../mirror/MirrorView.tsx";
import {
  getStudio,
  patchStudio,
  pressStudio,
  rewindStudio,
  studioDraftLog,
  type DraftLogEntry,
  type PressMode,
  type StudioPatch,
  type StudioPressResult,
  type StudioWire,
} from "./api.ts";
import type { ImagegenDraft } from "./draft.ts";
import {
  agentTouched,
  draftPatch,
  foldSince,
  formFromStudio,
  lastSeq,
  mergeForm,
  studioFromForm,
  studioKeysOf,
  studioSignal,
  toggleLock as toggled,
  type StudioKey,
} from "./studioSync.ts";

const POLL_MS = 2000;
const DEBOUNCE_MS = 500;

// Where the signal left off, per (studio, session): the agent that was attached last must not
// have its "since" reset by a remount, and a newly attached one starts from where it joined.
// Keyed by both ids, so a test (or a second studio) never reads another's position.
const signalled = new Map<string, number>();

export interface StudioState {
  studio: StudioWire | null;
  /** The studio could not be read: gone (404) or the Agent refused. */
  failed: string | null;
  form: ImagegenDraft;
  patchForm: (p: Partial<ImagegenDraft>) => void;
  locks: string[];
  toggleLock: (k: StudioKey) => void;
  /** Keys the agent moved since the member last touched them. */
  highlight: ReadonlySet<StudioKey>;
  /** The edit log known so far, newest first. */
  log: DraftLogEntry[];
  hasOlder: boolean;
  loadOlder: () => Promise<void>;
  press: (mode: PressMode) => Promise<StudioPressResult | null>;
  rewind: (seq: number) => Promise<boolean>;
  setAgentTrial: (on: boolean) => void;
  /** Versions whose press_result did not append ("record pending"). */
  recordPending: ReadonlySet<string>;
  reload: () => Promise<void>;
  signal: MirrorSignal;
}

export function useStudio(id: string, opts: { running: boolean }): StudioState {
  const tr = useT();
  const toast = useToast();
  const [studio, setStudio] = useState<StudioWire | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [form, setForm] = useState<ImagegenDraft>(() => formFromStudio(null));
  const [highlight, setHighlight] = useState<Set<StudioKey>>(() => new Set());
  const [older, setOlder] = useState<DraftLogEntry[]>([]);
  const [olderCursor, setOlderCursor] = useState<number | null>(null);
  const [recordPending, setRecordPending] = useState<Set<string>>(() => new Set());

  const baseRef = useRef<StudioWire | null>(null);
  const formRef = useRef(form);
  formRef.current = form;
  const dirtyRef = useRef(false);
  const timerRef = useRef(0);
  const inflightRef = useRef<Promise<void> | null>(null);
  const seenSeqRef = useRef(-1);

  // How a fresh studio reaches the form: replace it (first read, rewind, lost race), merge the
  // keys that moved (a poll), or keep it (the member typed while a save was in flight).
  const adopt = useCallback((next: StudioWire, form: "replace" | "merge" | "keep") => {
    const prevSeen = seenSeqRef.current;
    baseRef.current = next;
    setStudio(next);
    setFailed(null);
    const seq = lastSeq(next.recent_log);
    // The first read is the baseline: what the agent did before this pane opened is not news.
    if (prevSeen >= 0) {
      const touched = agentTouched(next.recent_log, prevSeen);
      if (touched.length) setHighlight((h) => new Set([...h, ...touched]));
    }
    seenSeqRef.current = Math.max(prevSeen, seq);
    if (form === "replace") setForm(formFromStudio(next.draft));
    else if (form === "merge") setForm((f) => mergeForm(f, next.draft));
  }, []);

  const read = useCallback(async (): Promise<StudioWire | null> => {
    try {
      const r = await getStudio(id);
      if (!r || r.error) {
        setFailed(r?.error ? errText(r.error) || tr("imggen.studio_read_failed") : tr("imggen.studio_read_failed"));
        return null;
      }
      return r;
    } catch {
      return null; // a transient 502 while the Agent restarts keeps what is on screen
    }
  }, [id, tr]);

  const reload = useCallback(async () => {
    const r = await read();
    if (r) adopt(r, baseRef.current == null ? "replace" : "merge");
  }, [read, adopt]);

  useEffect(() => {
    if (!opts.running) return;
    void reload();
  }, [opts.running, reload]);

  // One PUT, carrying the form's pending draft edit and any `extra` (locks, the trial toggle).
  const put = useCallback(
    async (extra: Omit<StudioPatch, "author"> = {}): Promise<void> => {
      const base = baseRef.current;
      if (!base) return;
      const sentForm = formRef.current;
      const draft = dirtyRef.current ? draftPatch(base.draft, { ...studioFromForm(sentForm), suggest_model: base.draft.suggest_model }) : null;
      dirtyRef.current = false;
      if (!draft && !Object.keys(extra).length) return;
      const body: StudioPatch = { author: "human", ...extra, ...(draft ? { draft } : {}) };
      let r;
      try {
        r = await patchStudio(id, body, base.updated_at);
      } catch {
        dirtyRef.current = true; // network: keep it for the next attempt
        return;
      }
      if (!r || r.error) {
        // Lost the race, or refused: the studio as it now stands wins, and the member sees it.
        toast(r?.error ? errText(r.error) || tr("imggen.studio_save_failed") : tr("imggen.studio_save_failed"), { kind: "error" });
        const fresh = await read();
        if (fresh) adopt(fresh, "replace");
        return;
      }
      for (const d of r.dropped || []) {
        toast(
          tr(d.reason === "locked" ? "imggen.dropped_locked" : d.reason === "human_only" ? "imggen.dropped_human_only" : "imggen.dropped_invalid", {
            field: d.field,
            detail: d.detail || "",
          }),
          { kind: "info" },
        );
      }
      // Keep what the member typed while this was in flight; the next save carries it.
      const typedSince = formRef.current !== sentForm;
      adopt(r.studio, typedSince ? "keep" : "merge");
      if (typedSince) dirtyRef.current = true;
    },
    [id, read, adopt, toast, tr],
  );

  const flush = useCallback(async (): Promise<void> => {
    window.clearTimeout(timerRef.current);
    timerRef.current = 0;
    while (inflightRef.current) await inflightRef.current;
    if (!dirtyRef.current) return;
    const p = put().finally(() => {
      inflightRef.current = null;
    });
    inflightRef.current = p;
    await p;
  }, [put]);

  const patchForm = useCallback(
    (p: Partial<ImagegenDraft>) => {
      // The ref moves now, not on the next render: a press right after a patch (a result card's
      // "again with this seed") flushes before React has rendered, and must send the new value.
      const next = { ...formRef.current, ...p };
      formRef.current = next;
      setForm(next);
      const touched = studioKeysOf(Object.keys(p) as (keyof ImagegenDraft)[]);
      setHighlight((h) => (touched.some((k) => h.has(k)) ? new Set([...h].filter((k) => !touched.includes(k))) : h));
      dirtyRef.current = true;
      window.clearTimeout(timerRef.current);
      timerRef.current = window.setTimeout(() => void flush(), DEBOUNCE_MS);
    },
    [flush],
  );

  // Unsent edits are sent on the way out rather than dropped with the pane.
  useEffect(
    () => () => {
      if (dirtyRef.current) void flush();
    },
    [flush],
  );

  // The agent's edits. Only with a session bound, only while shown, never over an unsent edit.
  const bound = !!studio?.session;
  useEffect(() => {
    if (!bound || !opts.running) return;
    const tick = async () => {
      if (document.hidden || dirtyRef.current || inflightRef.current) return;
      const r = await read();
      if (!r || dirtyRef.current || inflightRef.current) return;
      if (r.updated_at !== baseRef.current?.updated_at || r.session !== baseRef.current?.session) adopt(r, "merge");
    };
    const t = window.setInterval(() => void tick(), POLL_MS);
    return () => window.clearInterval(t);
  }, [bound, opts.running, read, adopt]);

  const toggleLock = useCallback(
    (k: StudioKey) => {
      const base = baseRef.current;
      if (!base) return;
      void (async () => {
        await flush();
        await put({ locks: toggled(baseRef.current?.locks, k) });
      })();
    },
    [flush, put],
  );

  const setAgentTrial = useCallback(
    (on: boolean) => {
      void (async () => {
        await flush();
        await put({ agent_trial: on });
      })();
    },
    [flush, put],
  );

  const press = useCallback(
    async (mode: PressMode): Promise<StudioPressResult | null> => {
      await flush();
      let r: StudioPressResult;
      try {
        r = await pressStudio(id, mode);
      } catch {
        toast(tr("imggen.enqueue_failed"), { kind: "error" });
        return null;
      }
      if (!r || r.error) {
        toast((r?.error && errText(r.error)) || tr("imggen.enqueue_failed"), { kind: "error" });
        return null;
      }
      if (r.recorded === false && r.version) setRecordPending((s) => new Set([...s, r.version]));
      void reload();
      return r;
    },
    [id, flush, reload, toast, tr],
  );

  const rewind = useCallback(
    async (seq: number): Promise<boolean> => {
      await flush();
      try {
        const r = await rewindStudio(id, seq);
        if (!r || r.error) {
          toast((r?.error && errText(r.error)) || tr("imggen.rewind_failed"), { kind: "error" });
          return false;
        }
        // A rewind is the member's: the whole draft is theirs again, outlines included.
        setHighlight(new Set());
        adopt(r, "replace");
        toast(tr("imggen.rewound", { n: seq }), { kind: "info" });
        return true;
      } catch {
        toast(tr("imggen.rewind_failed"), { kind: "error" });
        return false;
      }
    },
    [id, flush, adopt, toast, tr],
  );

  const loadOlder = useCallback(async () => {
    const recent = baseRef.current?.recent_log || [];
    const before = olderCursor ?? (recent.length ? Math.min(...recent.map((e) => e.seq)) : 0);
    if (!before) return;
    try {
      const page = await studioDraftLog(id, before, 50);
      if (!page || page.error) return;
      setOlder((o) => [...o, ...(page.entries || [])]);
      setOlderCursor(page.before ?? 0);
    } catch {
      /* the button stays for another try */
    }
  }, [id, olderCursor]);

  const log = useMemo(() => {
    const bySeq = new Map<number, DraftLogEntry>();
    for (const e of [...older, ...(studio?.recent_log || [])]) bySeq.set(e.seq, e);
    return [...bySeq.values()].sort((a, b) => b.seq - a.seq);
  }, [older, studio?.recent_log]);
  const recentCount = studio?.recent_log?.length || 0;
  const hasOlder = olderCursor === null ? recentCount >= 20 : olderCursor > 0;

  const logRef = useRef(log);
  logRef.current = log;
  const session = studio?.session || "";
  // The agent joined at the log as it stood then; what happens after is what it has not seen.
  useEffect(() => {
    const key = `${id}:${session}`;
    if (session && !signalled.has(key)) signalled.set(key, lastSeq(logRef.current));
  }, [id, session]);
  const signal = useMemo<MirrorSignal>(() => {
    const key = `${id}:${session}`;
    let pendingSeq = 0;
    return {
      line: () => {
        const entries = logRef.current;
        if (!signalled.has(key)) signalled.set(key, lastSeq(entries));
        const since = foldSince(entries, signalled.get(key) ?? 0);
        pendingSeq = since.seq;
        return studioSignal(since, {
          draftChanged: tr("imggen.signal_draft_changed"),
          newResults: tr("imggen.signal_new_results", { n: "{n}" }),
          rewind: tr("imggen.signal_rewind", { n: "{n}" }),
        });
      },
      sent: () => {
        signalled.set(key, pendingSeq);
      },
    };
  }, [id, session, tr]);

  return {
    studio,
    failed,
    form,
    patchForm,
    locks: studio?.locks || [],
    toggleLock,
    highlight,
    log,
    hasOlder,
    loadOlder,
    press,
    rewind,
    setAgentTrial,
    recordPending,
    reload,
    signal,
  };
}
