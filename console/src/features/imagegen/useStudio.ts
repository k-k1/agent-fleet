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
  rebaseForm,
  studioFromForm,
  studioKeysOf,
  studioSignal,
  toggleLock as toggled,
  type StudioKey,
} from "./studioSync.ts";

const POLL_MS = 2000;
const DEBOUNCE_MS = 500;
/** A save that got no answer (network, 5xx) is retried on this schedule, then left dirty. */
const RETRY_MS = [2000, 5000, 15000, 30000];
/** The first read of a studio, retried while the Agent restarts (a 502) rather than given up. */
const FIRST_READ_RETRY_MS = 3000;

// Per-studio state that outlives the pane (a reopen, a reload): the edit-log position the pane
// has seen and the outlines the member has not cleared yet (decision 6), and per (studio,
// session) where the signal left off. In localStorage, keyed by the ids, so two studios or two
// agents never read each other's.
interface Seen {
  seq: number;
  keys: string[];
}
const seenKey = (id: string) => `af.imagegen-seen.${id}`;
const signalKey = (id: string, session: string) => `af.imagegen-signal.${id}.${session}`;

function readJSON<T>(key: string): T | null {
  try {
    const v = localStorage.getItem(key);
    return v ? (JSON.parse(v) as T) : null;
  } catch {
    return null;
  }
}

function writeJSON(key: string, v: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* a blocked store only forgets the outlines */
  }
}

export interface StudioState {
  studio: StudioWire | null;
  /** The studio could not be read: gone (404) or the Agent refused. */
  failed: string | null;
  /** The Agent answered that there is no such studio. */
  missing: boolean;
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
  const [missing, setMissing] = useState(false);
  const [form, setForm] = useState<ImagegenDraft>(() => formFromStudio(null));
  const [highlight, setHighlight] = useState<Set<StudioKey>>(
    () => new Set((id ? readJSON<Seen>(seenKey(id))?.keys || [] : []) as StudioKey[]),
  );
  const [older, setOlder] = useState<DraftLogEntry[]>([]);
  const [olderCursor, setOlderCursor] = useState<number | null>(null);
  const [recordPending, setRecordPending] = useState<Set<string>>(() => new Set());

  const baseRef = useRef<StudioWire | null>(null);
  const formRef = useRef(form);
  formRef.current = form;
  const dirtyRef = useRef(false);
  const timerRef = useRef(0);
  const inflightRef = useRef<Promise<void> | null>(null);
  const retryRef = useRef(0);
  // -1 = no baseline yet: the first read is the baseline unless this studio was seen before.
  const seenSeqRef = useRef(id ? (readJSON<Seen>(seenKey(id))?.seq ?? -1) : -1);

  useEffect(() => {
    if (id && seenSeqRef.current >= 0) writeJSON(seenKey(id), { seq: seenSeqRef.current, keys: [...highlight] });
  }, [id, highlight, studio?.updated_at]);

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
        const code = r?.error?.code || "";
        // A gateway error is the Agent restarting, not an answer about the studio.
        if (/bad_gateway|unavailable|timeout|agent_down/.test(code)) return null;
        setMissing(/not_found|no_studio/.test(code));
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

  // The first read, retried until it lands: a 502 while the Agent restarts must not leave the
  // pane on an empty form that the member then types over.
  useEffect(() => {
    if (!opts.running) return;
    let alive = true;
    let t = 0;
    const attempt = async () => {
      await reload();
      if (alive && !baseRef.current) t = window.setTimeout(() => void attempt(), FIRST_READ_RETRY_MS);
    };
    void attempt();
    return () => {
      alive = false;
      window.clearTimeout(t);
    };
  }, [opts.running, reload]);

  // One PUT, carrying the form's pending draft edit and any `extra` (locks, the trial toggle).
  // Three outcomes besides success, answered differently:
  //   - 412: the agent (or another pane) wrote first. Re-read, lay the member's pending keys back
  //     over the new studio, and send again against the new version — nothing typed is lost.
  //   - no answer / 5xx: keep the edit and retry on a backoff.
  //   - any other refusal: say so and show the studio as it stands.
  const put = useCallback(
    async (extra: Omit<StudioPatch, "author"> = {}, attempt = 0): Promise<void> => {
      const base = baseRef.current;
      if (!base) return;
      const sentForm = formRef.current;
      const draft = dirtyRef.current ? draftPatch(base.draft, { ...studioFromForm(sentForm), suggest_model: base.draft.suggest_model }) : null;
      dirtyRef.current = false;
      if (!draft && !Object.keys(extra).length) return;
      const body: StudioPatch = { author: "human", ...extra, ...(draft ? { draft } : {}) };
      const r = await patchStudio(id, body, base.updated_at);
      if (r.status === 412) {
        const fresh = await read();
        if (!fresh) {
          if (draft) dirtyRef.current = true;
          scheduleRetry();
          return;
        }
        const pending = Object.keys(draftPatch(base.draft, studioFromForm(formRef.current)) || {});
        adopt(fresh, "keep");
        const rebased = rebaseForm(formRef.current, pending, fresh.draft);
        formRef.current = rebased;
        setForm(rebased);
        if (pending.length) dirtyRef.current = true;
        if (attempt < 2) await put(extra, attempt + 1);
        else scheduleRetry();
        return;
      }
      if (r.status === 0 || r.status >= 500) {
        if (draft) dirtyRef.current = true;
        if (!draft) toast(tr("imggen.studio_save_failed"), { kind: "error" });
        else scheduleRetry();
        return;
      }
      if (r.error || r.status >= 400 || !r.studio) {
        toast(r.error ? errText(r.error) || tr("imggen.studio_save_failed") : tr("imggen.studio_save_failed"), { kind: "error" });
        const fresh = await read();
        if (fresh) adopt(fresh, "replace");
        return;
      }
      retryRef.current = 0;
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
    // scheduleRetry is hoisted below and only reads refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [id, read, adopt, toast, tr],
  );

  // The debounce, re-armed after a save that got no answer — so a failed save is not left
  // sitting dirty (which also holds the poll off) until the member happens to type again.
  function scheduleRetry() {
    const wait = RETRY_MS[Math.min(retryRef.current, RETRY_MS.length - 1)];
    retryRef.current++;
    window.clearTimeout(timerRef.current);
    timerRef.current = window.setTimeout(() => void flushRef.current(), wait);
  }

  const flushRef = useRef<() => Promise<void>>(async () => {});
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
  flushRef.current = flush;

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
  const hasLog = !!studio;
  useEffect(() => {
    if (!id || !session || !hasLog) return;
    const key = signalKey(id, session);
    if (readJSON<number>(key) == null) writeJSON(key, lastSeq(logRef.current));
  }, [id, session, hasLog]);
  const signal = useMemo<MirrorSignal>(() => {
    const key = signalKey(id, session);
    let pendingSeq = 0;
    return {
      line: () => {
        const entries = logRef.current;
        const from = readJSON<number>(key) ?? lastSeq(entries);
        const since = foldSince(entries, from);
        pendingSeq = since.seq;
        return studioSignal(since, {
          draftChanged: tr("imggen.signal_draft_changed"),
          newResults: tr("imggen.signal_new_results", { n: "{n}" }),
          rewind: tr("imggen.signal_rewind", { n: "{n}" }),
        });
      },
      sent: () => {
        writeJSON(key, pendingSeq);
      },
    };
  }, [id, session, tr]);

  return {
    studio,
    failed,
    missing,
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
