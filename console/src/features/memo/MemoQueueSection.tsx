// MemoQueueSection (docs/log/21, UI刷新) — the left-pane memo queue. Notes accumulate per
// membership and sync across devices (Control-Plane persisted, no server push → refetch
// on mount / store bump + slow poll while mounted). The revamp:
//   - the composer is hidden by default; a header "+" or leader Ctrl/⌘+K → M reveals it;
//   - a queued memo is editable in place (click to expand full text, click again to edit;
//     the Collapse control under the body, or the row menu, closes it again);
//   - categories are first-class (add empty, rename, delete) and everything reorders by
//     drag — memos within/between categories, and the categories themselves;
//   - the send button (「送信…」) opens SendMemoModal to edit the concatenated text and
//     pick a destination.
import { useFold } from "../../lib/useFold.ts";
import { usePublishedHeight } from "../../lib/usePublishedHeight.ts";
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ComponentProps, MouseEvent as RMouseEvent, DragEvent as RDragEvent, RefObject } from "react";
import { createPortal } from "react-dom";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { copyText } from "../../lib/clipboard.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT, type MsgKey } from "../../lib/i18n/index.ts";
import {
  memoList,
  memoCreate,
  memoUpdate,
  memoDelete,
  memoCategoryList,
  memoCategoryCreate,
  memoCategoryUpdate,
  memoCategoryDelete,
  memoPasteImage,
  memoImageGC,
  memoQueueList,
  memoRelease,
  memoStopChain,
  memoQueueResume,
} from "./api.ts";
import { memoScope, useMemoStore } from "./store.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useReposStore } from "../repos/store.ts";
import { repoBaseName } from "../../lib/workingSets.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useDraft } from "../../lib/draft.ts";
import { autoGrowTextarea } from "../../lib/autoGrow.ts";
import { useSettings } from "../../lib/settings.ts";
import type { Memo, MemoCategory, MemoAttachment } from "../../types/memo.ts";
import { errText } from "../../core/api/client.ts";
import { MemoQueueSettingsModal } from "./MemoQueueSettingsModal.tsx";
import { useQueueSettings, useQueueSettingsCache } from "./queueSettingsCache.ts";
import { MemoProducerText } from "./MemoProducerText.tsx";
import { MemoQueueRuns } from "./MemoQueueRuns.tsx";
import { MemoQueueDeliveriesModal } from "./MemoQueueDeliveriesModal.tsx";
import { CatHeadMenu, type CatHeadMenuAt } from "./CatHeadMenu.tsx";
import { MemoReleaseCard } from "./MemoReleaseCard.tsx";
import { MemoReferenceLink } from "./MemoReferenceLink.tsx";
import { memoReference } from "./reference.ts";
import { confirmNotes, forgetNotes, trackNotes } from "./shownNotes.ts";
import { MemoQueueNotices } from "./MemoQueueNotices.tsx";
import { tidySelection } from "./tidyOps.ts";
import { effectiveTimeZone, fmtDateTime } from "../../lib/intl.ts";
import { afterChoices, afterItemMessage, continuationHeldKey, afterItemWords, deferredUntil, scheduledUntil, fmtStartAfter, startAfterFromInput, startAfterInputValue, isStaleRelease, isSuperseded, memoOriginWords, memoStateWords, pendingCount, readMemoQueues, releaseCards, releasedByTidy, staleIdsOf, tidyMarks } from "./queues.ts";
import { MemoTidyModal } from "./MemoTidyModal.tsx";
import { SendMemoModal } from "./SendMemoModal.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { consumeShare } from "./share.ts";
import { setMemoDragData } from "./dnd.ts";
import { canStartFromMemo, pendingMemoUploads, startFromMemo } from "./launchStash.ts";

const POLL_MS = 10000;
const SECTION_KEY = "af-section-memos"; // shared with the Section component's own key

// A category group as rendered: its memos plus the backing category row id (undefined for
// a legacy category that only exists as a memo.category string, i.e. not reorderable).
interface Group {
  category: string;
  catId?: string;
  memos: Memo[];
}
interface RepoBlock {
  repo: string;
  groups: Group[];
}

const repoLabel = (repo: string) => repo || t("memo.common");
const catLabel = (cat: string) => cat || t("memo.uncategorized");
// Identity of a rendered group (fold state, drop highlight, composer placement).
const groupKey = (repo: string, category: string) => repo + "\x00" + category;

// Keep memo composition fields as compact as their content permits, while letting a
// longer note grow without the user having to drag the resize handle first.
function useAutosizeTextarea(ref: RefObject<HTMLTextAreaElement | null>, value: string) {
  useLayoutEffect(() => {
    autoGrowTextarea(ref.current);
  }, [ref, value]);
}

// Group memos into repo → ordered categories. Category ORDER comes from the categories
// table (by position); the uncategorized bucket leads, then any legacy memo-only category
// is appended. Within a category, memos keep their server order (repo, category, position).
function groupMemos(memos: Memo[], cats: MemoCategory[]): RepoBlock[] {
  const repos: RepoBlock[] = [];
  const repoIx = new Map<string, RepoBlock>();
  const ensureRepo = (repo: string) => {
    let rb = repoIx.get(repo);
    if (!rb) {
      rb = { repo, groups: [] };
      repoIx.set(repo, rb);
      repos.push(rb);
    }
    return rb;
  };
  // Common repo first so the personal/unfiled bucket stays at the top.
  ensureRepo("");

  const byKey = new Map<string, Group>(); // repo\x00category -> group
  const groupFor = (repo: string, category: string, catId?: string) => {
    const key = repo + "\x00" + category;
    let g = byKey.get(key);
    if (!g) {
      g = { category, catId, memos: [] };
      byKey.set(key, g);
      ensureRepo(repo).groups.push(g);
    } else if (catId && !g.catId) {
      g.catId = catId;
    }
    return g;
  };

  // Seed uncategorized buckets and table-defined categories first, in order. The repo
  // set must be settled BEFORE seeding — a repo that only appears on a memo/category
  // would otherwise be created lazily, leaving its uncategorized bucket AFTER the named
  // categories instead of leading.
  const repoSet = new Set<string>([""]);
  for (const c of cats) repoSet.add(c.repo);
  for (const m of memos) repoSet.add(m.repo);
  for (const repo of repoSet) groupFor(repo, ""); // uncategorized leads (pruned if empty)
  for (const c of cats) groupFor(c.repo, c.name, c.id);

  // Place memos (creating any legacy category groups after the seeded ones).
  for (const m of memos) groupFor(m.repo, m.category).memos.push(m);

  // Drop empty uncategorized buckets, but keep empty NAMED categories (that's the point).
  for (const rb of repos) rb.groups = rb.groups.filter((g) => g.category !== "" || g.memos.length > 0);
  return repos.filter((rb) => rb.groups.length > 0);
}

export const MemoQueueSection = memo(function MemoQueueSection() {
  const workspaceRunning = useWorkspaceStore((s) => s.state) === "running";
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const memosKey = useMemoStore((s) => s.tick);
  const bumpMemos = useMemoStore((s) => s.bump);
  const composeReq = useMemoStore((s) => s.composeReq);
  const reveal = useMemoStore((s) => s.reveal);
  const toast = useToast();
  const askConfirm = useConfirm();
  const tr = useT();

  const [memos, setMemos] = useState<Memo[]>([]);
  // The tenant the list in `memos` was read for (null before the first read). The change tracking
  // folds the list in only while it belongs to the tenant on screen: the empty list before the first
  // read, and the old tenant's list right after a switch, are not "every note is gone".
  const [memosTenant, setMemosTenant] = useState<string | null>(null);
  const [cats, setCats] = useState<MemoCategory[]>([]);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  // Where the bottom "Add category" files the new category: "" = unfiled, else a base repository.
  const [newCatRepo, setNewCatRepo] = useState("");
  const workRepos = useReposStore((st) => st.repos);
  // Worktree folders file under their base repo (as add_memo does), so offer each base once.
  const repoChoices = useMemo(() => [...new Set(workRepos.map(repoBaseName))].sort(), [workRepos]);
  // A selection that left the list (repo removed, tenant switched) falls back to unfiled for good,
  // so the same name reappearing later is not picked up silently.
  useEffect(() => {
    if (newCatRepo && !repoChoices.includes(newCatRepo)) setNewCatRepo("");
  }, [newCatRepo, repoChoices]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  // Collapsed category groups, keyed by repo\x00category and persisted so the fold state
  // survives reloads (clicking a category header toggles it).
  const [collapsedCats, setCollapsedCats] = useState<Record<string, boolean>>(() => {
    try {
      return JSON.parse(localStorage.getItem("af.memo-collapsed") || "{}");
    } catch {
      return {};
    }
  });
  const toggleCollapse = (key: string) =>
    setCollapsedCats((s) => {
      const n = { ...s, [key]: !s[key] };
      try {
        localStorage.setItem("af.memo-collapsed", JSON.stringify(n));
      } catch {
        /* private mode / quota — fold state just won't persist */
      }
      return n;
    });
  // Categories whose folded "Sent N" line is open. Session-only: a fold that reopened by itself would defeat it.
  const [sentOpenCats, setSentOpenCats] = useState<Record<string, boolean>>({});
  const [editing, setEditing] = useState<string | null>(null);
  // Drop the open flag of a category with nothing left to fold, or a later started note would arrive already open.
  useEffect(() => {
    setSentOpenCats((o) => {
      const live = new Set<string>();
      for (const m of memos) if ((m.state === "delivered" || m.state === "superseded") && m.id !== editing) live.add(groupKey(m.repo, m.category));
      const keys = Object.keys(o).filter((k) => !live.has(k));
      if (keys.length === 0) return o;
      const n = { ...o };
      for (const k of keys) delete n[k];
      return n;
    });
  }, [memos, editing]);
  const [renameCat, setRenameCat] = useState<string | null>(null); // category id being renamed
  const [composerOpen, setComposerOpen] = useState(false);
  // Where the composer writes. null = the section-top composer, whose free-form category
  // field decides; a target = the composer moved under that category header ("+" on the
  // row), writing straight into that (repo, category).
  const [composeTarget, setComposeTarget] = useState<{ repo: string; category: string } | null>(null);
  const [newText, setNewText] = useDraft("af.memo-draft");
  const [newCat, setNewCat] = useDraft("af.memo-draft-cat");
  // Images attached to the memo being composed (uploaded to the container immediately;
  // committed into the memo on Add). Not draft-persisted — the bytes live server-side
  // and a page reload would leave them orphaned, so a fresh composer starts empty.
  const [newImages, setNewImages] = useState<MemoAttachment[]>([]);
  const [imgBusy, setImgBusy] = useState(false);
  const imgInputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [tidy, setTidy] = useState<Memo[] | null>(null);
  // Auto-run settings (ADR 0137), read with the memo poll below. tenantEnabled false: the
  // tenant's administrator has not allowed new settings.
  // The settings live in the cache the Auto-run view shares (queueSettingsCache.ts).
  const queueKey = (tenant ?? "") + "\u0000" + identity;
  const { queues, tenantEnabled, known: tenantKnown } = useQueueSettings(queueKey);
  // The dialogs hold a category id, never a copy of the category or its settings: each render
  // looks the live row up, so a poll that renames or removes it is seen by the dialog.
  const [settingsFor, setSettingsFor] = useState<string | null>(null);
  const [deliveriesFor, setDeliveriesFor] = useState<string | null>(null);
  // The history was opened by a "Run now" that found every slot taken: it names who holds them.
  const [slotsFullFor, setSlotsFullFor] = useState(false);
  // Categories whose inline run list is expanded. Not persisted: it reads the Control Plane on
  // open, so a reload starts quiet.
  const [runsOpen, setRunsOpen] = useState<Record<string, boolean>>({});
  // The tidy run the history opened for, so that its row stands out.
  const [focusRun, setFocusRun] = useState("");
  const [releasing, setReleasing] = useState<string | null>(null); // release card key in flight
  const [changedIds, setChangedIds] = useState<ReadonlySet<string>>(new Set());
  // What the cards have shown of each held note lives in shownNotes.ts, outside this component, so
  // drawing the section again does not forget it. Confirming bumps this counter to redraw.
  const [shownTick, setShownTick] = useState(0);
  const [resuming, setResuming] = useState(false);
  const [sendOpen, setSendOpen] = useState(false);
  // Section open/closed, owned here (controlled) so the leader command can force it open.
  const [openStored, setSectionOpen] = useFold(SECTION_KEY);
  const open = openStored !== false;
  const setOpen = setSectionOpen;
  const serRef = useRef("");
  const composerTextRef = useRef<HTMLTextAreaElement>(null);
  useAutosizeTextarea(composerTextRef, newText);

  // While mounted this list is the one the activity bar's badge counts from (store.ts): the bar's
  // own feed stands down, so api/memos is read by one timer.
  // App mounts one panel; the badge's hand-over assumes that. A second instance would poll on its
  // own timer too, and sharing one poller across instances is not worth the machinery for a case
  // the app does not have.
  useEffect(() => useMemoStore.getState().attachPanel(), []);

  // Refetch memos + categories on mount / bump / tenant switch, and poll while mounted.
  useEffect(() => {
    let alive = true;
    const scope = memoScope(workspaceRunning, tenant ?? "");
    const load = () => {
      // Numbered when the request starts, and tied to the scope it starts in (store.ts).
      const ticket = useMemoStore.getState().begin();
      const queueTicket = useQueueSettingsCache.getState().begin();
      // Published by its own read, so that a failed or slow memo or category read neither holds it
      // back nor drops it: Run now's enabled state follows it. A failed settings read (an older
      // Control Plane, an error body) keeps the cached one and never takes the memo list down.
      const queues = memoQueueList()
        .catch(() => null)
        .then((qRaw) => {
          if (!alive) return;
          const fresh = readMemoQueues(qRaw);
          if (fresh) useQueueSettingsCache.getState().publish(queueKey, queueTicket, fresh);
        });
      return Promise.all([memoList(), memoCategoryList(), queues])
        .then(([mList, cList]) => {
          if (!alive) return;
          // An error body (api() resolves HTTP errors as {error}) is a failed read, not an empty
          // list: keep what is on screen, and with it the records of the changed notes.
          const readOk = (v: unknown) => v === null || Array.isArray(v);
          if (!readOk(mList) || !readOk(cList)) return;
          const marr = Array.isArray(mList) ? mList : [];
          const carr = Array.isArray(cList) ? cList : [];
          useMemoStore.getState().publish(marr, ticket, scope);
          setMemosTenant(tenant ?? "");
          const ser = JSON.stringify([marr, carr]);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setMemos(marr);
            setCats(carr);
          }
        })
        .catch(() => {});
    };
    load();
    const id = setInterval(() => {
      if (!document.hidden) load(); // hidden tab: skip the tick (mobile data / battery)
    }, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [memosKey, tenant, identity, workspaceRunning]);

  // Leader Ctrl/⌘+K → M (or any requestCompose) reveals + focuses the composer.
  useEffect(() => {
    if (composeReq === 0) return;
    setOpen(true);
    setComposeTarget(null);
    setComposerOpen(true);
  }, [composeReq]);

  const blocks = useMemo(() => groupMemos(memos, cats), [memos, cats]);
  // What a card showed first, against what the poll serves now (shownNotes.ts). Idempotent for one
  // list, so a double render marks nothing new.
  const { edited, awaiting } = useMemo(
    () => (memosTenant === (tenant ?? "") ? trackNotes(tenant ?? "", memos) : { edited: new Set<string>(), awaiting: new Set<string>() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [memos, memosTenant, tenant, shownTick],
  );
  // The composer only follows a target that is still on screen: a category deleted (or
  // renamed) while it was open would otherwise leave an open composer nowhere to render.
  const target = useMemo(() => {
    if (!composeTarget) return null;
    const key = groupKey(composeTarget.repo, composeTarget.category);
    const alive = blocks.some((rb) => rb.groups.some((g) => groupKey(rb.repo, g.category) === key));
    return alive ? composeTarget : null;
  }, [composeTarget, blocks]);
  const targetKey = target ? groupKey(target.repo, target.category) : "";
  const unsent = useMemo(() => memos.filter((m) => !m.sentAt), [memos]);
  const catNames = useMemo(() => [...new Set(cats.map((c) => c.name))], [cats]);
  const selectedIds = useMemo(() => memos.filter((m) => sel[m.id]).map((m) => m.id), [memos, sel]);
  const selectedMemos = useMemo(() => memos.filter((m) => sel[m.id]), [memos, sel]);

  const toggle = (id: string) => setSel((s) => ({ ...s, [id]: !s[id] }));
  const queueByCat = useMemo(() => new Map(queues.map((q) => [q.categoryId, q])), [queues]);

  // Approve what a release card showed, each message with the hash its row carried. A 409
  // means something changed after the card was drawn: nothing was approved, the stale ids
  // are marked, and the list is re-read so the card shows what is there now.
  const stopChain = async (m: Memo) => {
    try {
      const res = await memoStopChain(m.id);
      if (res?.error) toast(errText(res.error) || t("memo.continuation_stop_failed"), { kind: "warn" });
      else toast(t("memo.continuation_stopped"), { kind: "success" });
    } catch {
      toast(t("memo.continuation_stop_failed"), { kind: "warn" });
    }
    bumpMemos();
  };

  const release = async (cardKey: string, items: Memo[]) => {
    if (releasing) return;
    setReleasing(cardKey);
    try {
      const res = await memoRelease(items.map((m) => ({ id: m.id, contentHash: m.contentHash ?? "" })));
      if (res?.error) {
        if (isStaleRelease(res)) {
          setChangedIds(new Set(staleIdsOf(res)));
          toast(t("memo.release_stale"), { kind: "warn" });
        } else toast(errText(res.error) || t("memo.release_failed"), { kind: "warn" });
        bumpMemos();
        return;
      }
      forgetNotes(tenant ?? "", items);
      setChangedIds(new Set());
      toast(t("memo.release_done", { count: items.length }), { kind: "success" });
      bumpMemos();
    } catch {
      toast(t("memo.release_failed"), { kind: "warn" });
    } finally {
      setReleasing(null);
    }
  };

  // The person has read what changed: the next press on the card approves the text drawn now.
  const confirmChange = (items: Memo[]) => {
    confirmNotes(tenant ?? "", items);
    setShownTick((n) => n + 1);
  };

  // Lifts a category's pause (the Control Plane's circuit breaker). Resolves true when lifted.
  const resume = async (categoryId: string): Promise<boolean> => {
    setResuming(true);
    try {
      const res = await memoQueueResume(categoryId);
      if (res?.error) {
        toast(errText(res.error) || t("memo.paused_resume_failed"), { kind: "warn" });
        return false;
      }
      toast(t("memo.paused_resume_done"), { kind: "success" });
      bumpMemos();
      return true;
    } catch {
      toast(t("memo.paused_resume_failed"), { kind: "warn" });
      return false;
    } finally {
      setResuming(false);
    }
  };

  // A notification asked to show a category: open the section, unfold the category, bring it into
  // view, and open its run history for the kinds that ask what happened to a run.
  const revealN = useRef(reveal.n);
  useEffect(() => {
    if (reveal.n === revealN.current || !reveal.id) return;
    const c = cats.find((x) => x.id === reveal.id);
    // Not loaded yet: the effect runs again when the categories arrive.
    if (!c) return;
    revealN.current = reveal.n;
    setOpen(true);
    setCollapsedCats((s) => ({ ...s, [groupKey(c.repo, c.name)]: false }));
    if (reveal.history) {
      setSlotsFullFor(!!reveal.slotsFull);
      setDeliveriesFor(c.id);
    }
    requestAnimationFrame(() => {
      document.querySelector(`[data-memo-cat="${CSS.escape(c.id)}"]`)?.scrollIntoView({ block: "nearest" });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal.n, cats]);

  // ---- composer ----------------------------------------------------------------
  const openComposer = () => {
    setComposeTarget(null);
    setComposerOpen(true);
  };
  // "+" on a category header: write into that group, with the composer rendered under its
  // header (and the group unfolded, or the composer would be hidden with its memos).
  const openComposerFor = (repo: string, category: string) => {
    setComposeTarget({ repo, category });
    setComposerOpen(true);
    const key = groupKey(repo, category);
    if (collapsedCats[key]) toggleCollapse(key);
  };
  // Focus on open AND whenever the composer moves to another category: it is one node
  // re-parented into the target group, so it remounts and loses focus. Also on every compose
  // request: the composer may already be open with a draft from an earlier visit, and then
  // neither of the other two changes, while the keystrokes that follow the shortcut would go
  // wherever focus was left.
  useEffect(() => {
    if (composerOpen) requestAnimationFrame(() => composerTextRef.current?.focus());
  }, [composerOpen, targetKey, composeReq]);

  // Upload image files (paste / drop / picker) into the container's memo-images dir and
  // append them to the composer's attachments. Non-image files are ignored — memo
  // attachments are images only (a screenshot shared from a phone, a dragged picture).
  const attachImages = async (files: Iterable<File>) => {
    const imgs = [...files].filter((f) => f.type.startsWith("image/"));
    if (!imgs.length) return;
    setImgBusy(true);
    try {
      for (const f of imgs) {
        const res = await memoPasteImage(f);
        if (res.status === 201 && res.path && res.name) {
          setNewImages((cur) => [...cur, { path: res.path!, name: res.name! }]);
        } else {
          toast(res.status === 413 ? t("memo.image_too_large") : t("memo.image_failed"));
        }
      }
    } catch {
      toast(t("memo.image_failed"));
    } finally {
      setImgBusy(false);
    }
  };
  const removeNewImage = (name: string) => setNewImages((cur) => cur.filter((a) => a.name !== name));

  // Best-effort prune of orphaned container images: keep everything referenced by a memo
  // plus the composer's in-flight uploads (not yet committed to a memo). Fire-and-forget.
  const runImageGC = (memoList: Memo[], pending: MemoAttachment[]) => {
    const keep = new Set<string>();
    for (const m of memoList) for (const a of m.attachments || []) keep.add(a.name);
    for (const a of pending) keep.add(a.name);
    for (const n of pendingMemoUploads()) keep.add(n);
    void memoImageGC([...keep]).catch(() => {});
  };

  const addMemo = async () => {
    const body = newText.trim();
    if ((!body && newImages.length === 0) || busy) return;
    setBusy(true);
    try {
      // A targeted composer writes into its own group; the section-top one takes the
      // repo-less common bucket and whatever the category field says.
      const repo = target ? target.repo : "";
      const category = target ? target.category : newCat.trim();
      const res = await memoCreate({
        kind: "text",
        body,
        repo,
        category,
        ...(newImages.length ? { attachments: newImages } : {}),
      });
      if ((res as { error?: unknown }).error) {
        toast(t("memo.add_failed"));
        return;
      }
      // The Control Plane makes the category row when the note arrives under a new name.
      setNewText("");
      setNewImages([]);
      bumpMemos();
    } catch {
      toast(t("memo.add_failed"));
    } finally {
      setBusy(false);
    }
  };

  // One-shot: a PWA share (the Android share sheet) launches the app at ?share=<id>. Pull the
  // shared text + images out of the SW stash, reveal the composer, and prefill it.
  useEffect(() => {
    let alive = true;
    void consumeShare().then((s) => {
      if (!alive || !s || (!s.text && s.files.length === 0)) return;
      setOpen(true);
      setComposeTarget(null);
      setComposerOpen(true);
      if (s.text) setNewText((prev) => (prev ? prev + "\n" + s.text : s.text));
      if (s.files.length) void attachImages(s.files);
    });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ---- memo edit / delete ------------------------------------------------------
  const saveEdit = async (m: Memo, body: string, category: string, clearRef: boolean, after?: string[], startAfter?: string) => {
    setEditing(null);
    try {
      // The reference is left out unless the member removed it: absent keeps it, `{}` clears it. The
      // notes it waits for are sent only when the member changed them: absent keeps them, [] clears.
      const res = await memoUpdate(m.id, {
        body: body.trim(),
        category: category.trim(),
        ...(clearRef ? { reference: {} } : {}),
        ...(after ? { after } : {}),
        ...(startAfter !== undefined ? { startAfter } : {}),
      });
      // apiJSON resolves a server error as {error} rather than throwing, so check it or a
      // false success toast is shown.
      if ((res as { error?: unknown }).error) {
        // A queue's own refusals (locked, sending, ...) have their own wording.
        const err = (res as unknown as { error: { code?: string } }).error;
        toast(err.code?.startsWith("memo_") ? errText(err) : t("common.send_failed"));
        return;
      }
      bumpMemos();
      toast(t("memo.updated"), { kind: "success" });
    } catch {
      toast(t("common.send_failed"));
    }
  };
  const remove = async (id: string) => {
    try {
      const res = await memoDelete(id);
      // Do not fall through to the GC below on failure: that memo's image leaves the keep
      // set, so the images of the very memos that survived a failed delete would be the
      // ones erased from the container.
      if (!res.ok) {
        toast(t("common.delete_failed"));
        return;
      }
      setSel((s) => {
        const n = { ...s };
        delete n[id];
        return n;
      });
      // Prune the deleted memo's images (keep every other memo's + composer's in-flight).
      // From a fresh list: the one on screen can predate a memo another surface just created
      // (a Start working stash, another device), whose files it would not know to keep. No
      // list, no sweep — the sweep is only hygiene.
      const fresh = await memoList().catch(() => null);
      if (Array.isArray(fresh)) runImageGC(fresh, newImages);
      bumpMemos();
    } catch {
      toast(t("common.delete_failed"));
    }
  };

  // ---- categories --------------------------------------------------------------
  // `repo` is the group the "+" sits in; "" files the row in the unfiled area. A queue is keyed by
  // (repo, category), so a row created without the repo cannot be used for repository work.
  const addCategory = async (repo = "") => {
    try {
      const res = await memoCategoryCreate({ repo, name: t("memo.new_category") });
      // apiJSON resolves a server error as {error} rather than throwing.
      if ((res as { error?: unknown }).error) {
        toast(t("memo.add_failed"));
        return;
      }
      bumpMemos();
      if (res && res.id) setRenameCat(res.id);
    } catch {
      toast(t("memo.add_failed"));
    }
  };
  const commitRename = async (c: MemoCategory, name: string) => {
    setRenameCat(null);
    const next = name.trim();
    if (!next || next === c.name) return;
    try {
      const res = await memoCategoryUpdate(c.id, { name: next });
      // apiJSON resolves a server error as {error} rather than throwing, so check it or the
      // failure is swallowed silently.
      if ((res as { error?: unknown }).error) {
        toast(t("common.send_failed"));
        return;
      }
      bumpMemos();
    } catch {
      toast(t("common.send_failed"));
    }
  };
  const deleteCategory = async (c: MemoCategory) => {
    const ok = await askConfirm({
      title: t("memo.delete_category"),
      body: t("memo.cat_delete_confirm", { name: c.name }),
      confirmLabel: t("common.delete"),
      danger: true,
    });
    if (!ok) return;
    try {
      // raw() resolves even on an HTTP error, so without checking res.ok this shows a false
      // success toast.
      const res = await memoCategoryDelete(c.id);
      if (!res.ok) {
        toast(t("common.delete_failed"));
        return;
      }
      bumpMemos();
      toast(t("memo.cat_deleted"));
    } catch {
      toast(t("common.delete_failed"));
    }
  };

  // ---- drag & drop -------------------------------------------------------------
  // Transient drag subjects (a memo id, or a category id). Native HTML5 DnD.
  const dragMemo = useRef<string | null>(null);
  const dragCat = useRef<string | null>(null);
  const [dropCat, setDropCat] = useState<string | null>(null); // repo\x00category highlighted

  // Persist the position (and repo/category) of every memo in the given groups, matching
  // the current local order — a few small PATCHes, then a bump to reconcile with the server.
  const persistGroups = async (list: Memo[], groups: { repo: string; category: string }[]) => {
    const patches: Promise<unknown>[] = [];
    for (const { repo, category } of groups) {
      const grp = list.filter((m) => m.repo === repo && m.category === category);
      // A replaced note is locked: the Control Plane refuses every write to it, which would undo the whole move.
      grp.forEach((m, i) => {
        if (!isSuperseded(m)) patches.push(memoUpdate(m.id, { position: i, category, repo }));
      });
    }
    let refused: unknown = null;
    try {
      // apiJSON resolves a refusal as {error} rather than throwing.
      for (const res of await Promise.all(patches)) {
        const err = (res as { error?: { code?: string } } | null)?.error;
        if (err && !refused) refused = err;
      }
    } finally {
      // The refetch puts a refused note back where the server still has it. The poll skips an
      // answer equal to the last one it applied, and a refused move changed nothing there, so the
      // optimistic order would stay on screen: forget the last answer to make it apply.
      if (refused) serRef.current = "";
      bumpMemos();
    }
    if (refused) {
      const err = refused as { code?: string };
      toast(err.code?.startsWith("memo_") ? errText(err) : t("common.send_failed"));
    }
    return !refused;
  };

  // Reorder a memo before another memo (adopting the target's repo/category if different).
  const dropMemoOnMemo = (dragId: string, targetId: string) => {
    if (dragId === targetId) return;
    const arr = memos.slice();
    const di = arr.findIndex((m) => m.id === dragId);
    const ti = arr.findIndex((m) => m.id === targetId);
    if (di < 0 || ti < 0) return;
    const [moved] = arr.splice(di, 1);
    const target = arr.find((m) => m.id === targetId)!;
    const changedGroups = [{ repo: moved.repo, category: moved.category }];
    // Never mutate an object inside the state array; replace it with a new one.
    const placed = { ...moved, repo: target.repo, category: target.category };
    const insertAt = arr.findIndex((m) => m.id === targetId);
    arr.splice(insertAt, 0, placed);
    setMemos(arr);
    changedGroups.push({ repo: target.repo, category: target.category });
    void persistGroups(arr, dedupeGroups(changedGroups));
  };

  // Move a memo to the end of a (repo, category) group.
  const dropMemoOnGroup = (dragId: string, repo: string, category: string) => {
    const arr = memos.slice();
    const di = arr.findIndex((m) => m.id === dragId);
    if (di < 0) return;
    const [moved] = arr.splice(di, 1);
    const from = { repo: moved.repo, category: moved.category };
    if (moved.repo === repo && moved.category === category) {
      arr.splice(di, 0, moved); // no-op drop onto own group
      return;
    }
    // Never mutate an object inside the state array; replace it with a new one.
    const placed = { ...moved, repo, category };
    arr.push(placed);
    setMemos(arr);
    void persistGroups(arr, dedupeGroups([from, { repo, category }])).then((ok) => {
      if (ok && category) toast(t("memo.moved", { cat: category }));
    });
  };

  // Reorder categories within a repo by rewriting positions.
  const dropCatOnCat = (dragId: string, targetId: string) => {
    if (dragId === targetId) return;
    const drag = cats.find((c) => c.id === dragId);
    const target = cats.find((c) => c.id === targetId);
    if (!drag || !target || drag.repo !== target.repo) return;
    const repoCats = cats.filter((c) => c.repo === drag.repo).slice();
    const di = repoCats.findIndex((c) => c.id === dragId);
    repoCats.splice(di, 1);
    const ti = repoCats.findIndex((c) => c.id === targetId);
    repoCats.splice(ti, 0, drag);
    // Optimistic update: mapping `prev` straight through leaves the element order unchanged
    // and is a no-op, so nothing shows until the refetch. Instead, fill the slots this
    // repo's categories occupy with the reordered repoCats, in order.
    setCats((prev) => {
      let i = 0;
      return prev.map((c) => (c.repo === drag.repo ? repoCats[i++] : c));
    });
    Promise.all(repoCats.map((c, i) => (c.position === i ? null : memoCategoryUpdate(c.id, { position: i }))))
      .catch(() => {})
      .finally(bumpMemos);
  };

  const selectGroup = (ids: string[]) => setSel((s) => {
    const n = { ...s };
    for (const id of ids) n[id] = true;
    return n;
  });
  const sendGroup = (ids: string[]) => {
    if (ids.length === 0) return;
    selectGroup(ids);
    setSendOpen(true);
  };

  // ---- per-row menu (⋯ kebab + right-click) ------------------------------------
  // A single menu, opened either at the cursor (context menu) or anchored under the
  // ⋯ button. Delete lives here so touch users get a deliberate two-step delete
  // instead of a one-tap trash button (mirrors the chat assistant rows).
  const [memoMenu, setMemoMenu] = useState<{ x: number; y: number; m: Memo; anchor?: DOMRect } | null>(null);
  const memoMenuRef = useRef<HTMLUListElement>(null);
  const openMemoMenu = (e: RMouseEvent, m: Memo) => {
    e.preventDefault();
    e.stopPropagation();
    setMemoMenu({ x: e.clientX, y: e.clientY, m });
  };
  const openMemoMenuBtn = (e: RMouseEvent, m: Memo) => {
    e.preventDefault();
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    setMemoMenu({ x: r.left, y: r.bottom + 2, m, anchor: r });
  };
  useDismiss(memoMenuRef, !!memoMenu, () => setMemoMenu(null));
  useMenuRoving(memoMenuRef, !!memoMenu);
  useLayoutEffect(() => {
    const el = memoMenuRef.current;
    if (!memoMenu || !el) return;
    const bounds = el.closest<HTMLElement>(".app-rail");
    if (memoMenu.anchor) placeFixed(el, memoMenu.anchor.right - el.offsetWidth, memoMenu.anchor.bottom + 2, bounds);
    else placeFixed(el, memoMenu.x, memoMenu.y, bounds);
  });
  // Category head menu (⋯, right-click, Menu / Shift+F10): one at a time, for the category named by `cat`.
  const [catMenu, setCatMenu] = useState<{ at: CatHeadMenuAt; catId: string } | null>(null);
  // Sendable ids per category, refreshed each render, so an open menu acts on the current list and not on the one it opened with.
  const catSendIds = useRef(new Map<string, string[]>());
  const runMemoMenu = (fn: () => void) => {
    setMemoMenu(null);
    fn();
  };

  const actions = (
    <>
      <Button
        small
        variant="ghost"
        icon="add"
        title={tr("memo.add_title")}
        aria-label={tr("memo.add_title")}
        onClick={openComposer}
      />
      <Button
        small
        variant="ghost"
        icon="sparkle"
        title={workspaceRunning ? tr("memo.organize_title") : tr("memo.organize_needs_ws")}
        aria-label={tr("memo.organize_title")}
        disabled={!workspaceRunning || selectedIds.length === 0}
        onClick={() => {
          if (selectedMemos.length === 0) {
            toast(t("memo.select_to_organize"), { kind: "warn" });
            return;
          }
          // A replaced note is read-only and is never a source of a tidy.
          const { memos: tidyable, dropped } = tidySelection(selectedMemos);
          if (tidyable.length === 0) {
            toast(t("memo.tidyop_replaced_all"), { kind: "warn" });
            return;
          }
          if (dropped > 0) toast(t("memo.tidyop_replaced_some", { count: dropped }));
          setTidy(tidyable);
        }}
      />
    </>
  );

  // One composer, rendered either at the top of the section or under the header of the
  // category it targets — never both, so the draft text and in-flight uploads stay put.
  const composer = composerOpen && (
    <div
      className={"memo-add" + (target ? " targeted" : "")}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files")) e.preventDefault();
      }}
      onDrop={(e) => {
        if (e.dataTransfer.files.length) {
          e.preventDefault();
          void attachImages(e.dataTransfer.files);
        }
      }}
    >
      <textarea
        ref={composerTextRef}
        className="memo-add-text"
        value={newText}
        rows={2}
        placeholder={tr("memo.add_ph")}
        onChange={(e) => setNewText(e.target.value)}
        onPaste={(e) => {
          const imgs = [...e.clipboardData.files].filter((f) => f.type.startsWith("image/"));
          if (imgs.length) {
            e.preventDefault();
            void attachImages(imgs);
          }
        }}
        onKeyDown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
            e.preventDefault();
            void addMemo();
          } else if (e.key === "Escape") {
            setComposerOpen(false);
          }
        }}
      />
      {(newImages.length > 0 || imgBusy) && (
        <div className="memo-add-thumbs">
          {newImages.map((a) => (
            <MemoImageThumb key={a.name} name={a.name} onRemove={() => removeNewImage(a.name)} />
          ))}
          {imgBusy && (
            <span className="memo-thumb loading">
              <Icon name="loading" spin />
            </span>
          )}
        </div>
      )}
      <div className="memo-add-row">
        {/* The category is the field only for the section-top composer; a targeted
            one sits under its category header, which says where the memo lands. */}
        {target ? (
          <span className="memo-add-target">{catLabel(target.category)}</span>
        ) : (
          <input
            className="memo-add-cat"
            list="memo-cat-suggest"
            value={newCat}
            placeholder={tr("memo.category_ph")}
            onChange={(e) => setNewCat(e.target.value)}
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                e.preventDefault();
                void addMemo();
              } else if (e.key === "Escape") setComposerOpen(false);
            }}
          />
        )}
        <input
          ref={imgInputRef}
          type="file"
          accept="image/*"
          multiple
          hidden
          onChange={(e) => {
            if (e.target.files?.length) void attachImages(e.target.files);
            e.target.value = "";
          }}
        />
        <Button
          small
          variant="ghost"
          title={tr("memo.attach_image")}
          aria-label={tr("memo.attach_image")}
          disabled={imgBusy}
          onClick={() => imgInputRef.current?.click()}
        >
          <Icon name="device-camera" />
        </Button>
        <Button
          small
          variant="primary"
          disabled={(!newText.trim() && newImages.length === 0) || busy}
          onClick={() => void addMemo()}
        >
          {tr("memo.add")}
        </Button>
      </div>
      <div className="memo-add-hint">{tr("memo.composer_hint")}</div>
      <datalist id="memo-cat-suggest">
        {catNames.map((c) => (
          <option key={c} value={c} />
        ))}
      </datalist>
    </div>
  );

  const addCatRow = (
    <span className="memo-addcat-row">
      <button
        type="button"
        className="memo-addcat"
        onClick={() => void addCategory(repoChoices.includes(newCatRepo) ? newCatRepo : "")}
      >
        <Icon name="add" /> {tr("memo.add_category")}
      </button>
      {repoChoices.length > 0 && (
        <select
          className="memo-addcat-repo"
          aria-label={tr("memo.add_category_where")}
          value={repoChoices.includes(newCatRepo) ? newCatRepo : ""}
          onChange={(e) => setNewCatRepo(e.target.value)}
        >
          <option value="">{tr("memo.common")}</option>
          {repoChoices.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      )}
    </span>
  );

  return (
    <>
      <Section
        id="memos"
        title={tr("memo.title")}
        icon="note"
        count={unsent.length}
        actions={actions}
        open={open}
        onToggle={() => setSectionOpen(!open)}
      >
        {!target && composer}

        {memos.length === 0 && cats.length === 0 ? (
          <>
            <div className="pane-empty">{tr("memo.empty")}</div>
            {addCatRow}
          </>
        ) : (
          <>
            {blocks.map((rb) => (
              <div key={rb.repo} className="memo-repo">
                {rb.repo && (
                  <MemoRepoHead repo={rb.repo} label={repoLabel(rb.repo)} onAdd={() => void addCategory(rb.repo)} />
                )}
                {rb.groups.map((g) => {
                  const cat = g.catId ? cats.find((c) => c.id === g.catId) : undefined;
                  const dropKey = groupKey(rb.repo, g.category);
                  const collapsed = !!collapsedCats[dropKey];
                  const queue = cat ? queueByCat.get(cat.id) : undefined;
                  const isQueue = !!queue || !!cat?.queue;
                  // Delivered and replaced notes are history, not work: a queue folds them into one line so the
                  // waiting ones lead. "Needs a look" (unconfirmed) stays in the list because it wants a decision.
                  // A note being edited stays visible: folding it would unmount the draft.
                  const folded = isQueue ? g.memos.filter((m) => (m.state === "delivered" || isSuperseded(m)) && m.id !== editing) : [];
                  const foldedIds = new Set(folded.map((m) => m.id));
                  const shown = folded.length > 0 ? g.memos.filter((m) => !foldedIds.has(m.id)) : g.memos;
                  const sentOpen = !!sentOpenCats[dropKey];
                  // Opening the fold adds the history below the fold line; the waiting notes and the line stay put.
                  // Newest first, like the Auto-run view's "Ended N".
                  const history = sentOpen
                    ? [...folded].sort((a, b) => histTime(b) - histTime(a))
                    : [];
                  const visible = [...shown, ...history];
                  // A replaced note is read-only and cannot be sent, and a folded one is not on screen,
                  // so a select-all covers neither.
                  const ids = visible.filter((m) => !isSuperseded(m)).map((m) => m.id);
                  if (cat) catSendIds.current.set(cat.id, ids);
                  const allSel = ids.length > 0 && ids.every((id) => sel[id]);
                  const renderRow = (m: Memo, isHistory = false) => (
                    <MemoRow
                      key={m.id}
                      memo={m}
                      selected={!!sel[m.id]}
                      expanded={!!expanded[m.id]}
                      editing={editing === m.id}
                      currentCat={g.category}
                      onToggle={() => toggle(m.id)}
                      onExpand={() => setExpanded((s) => ({ ...s, [m.id]: true }))}
                      onCollapse={() => setExpanded((s) => ({ ...s, [m.id]: false }))}
                      onEdit={() => setEditing(m.id)}
                      onCancelEdit={() => setEditing(null)}
                      onSave={(body, category, clearRef, after, startAfter) => void saveEdit(m, body, category, clearRef, after, startAfter)}
                      afterChoices={isQueue && (m.state === "held" || m.state === "pending") ? afterChoices(g.memos, m) : undefined}
                      onOpenTidyRuns={
                        cat && isQueue
                          ? () => {
                              setFocusRun(m.tidyRunId ?? "");
                              setDeliveriesFor(cat.id);
                            }
                          : undefined
                      }
                      continuationMax={queue?.continuationMax}
                      onStopChain={
                        // A root has no chain id: it can be stopped before its first follow-up exists, while the
                        // category allows follow-ups and the note a person or the switch approved points at a work item.
                        m.chainId || (isQueue && (queue?.continuationMax ?? 0) > 0 && memoReference(m) && !(m.releasedBy && m.releasedBy !== "setting"))
                          ? () => void stopChain(m)
                          : undefined
                      }
                      onOpenMenu={(e) => openMemoMenu(e, m)}
                      onOpenKebab={(e) => openMemoMenuBtn(e, m)}
                      onDragStart={(e) => {
                        dragMemo.current = m.id;
                        // Also carry the memo's text so a drop onto a session composer
                        // inserts it (a copy — the memo stays queued).
                        setMemoDragData(e.dataTransfer, m);
                      }}
                      onDragEnd={() => {
                        dragMemo.current = null;
                        setDropCat(null);
                      }}
                      onDropBefore={() => {
                        // History notes are not drop targets: their server position says nothing about the waiting order.
                        if (dragMemo.current && !isHistory) dropMemoOnMemo(dragMemo.current, m.id);
                      }}
                    />
                  );
                  const cards = releaseCards(g.memos);
                  return (
                    <div
                      key={g.category || "\x00unfiled"}
                      data-memo-cat={cat?.id}
                      className={"memo-cat" + (dropCat === dropKey ? " drop" : "")}
                      onDragOver={(e) => {
                        if (dragMemo.current || dragCat.current) {
                          e.preventDefault();
                          setDropCat(dropKey);
                        }
                      }}
                      onDragLeave={(e) => {
                        if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setDropCat(null);
                      }}
                      onDrop={(e) => {
                        e.preventDefault();
                        setDropCat(null);
                        if (dragMemo.current) dropMemoOnGroup(dragMemo.current, rb.repo, g.category);
                        else if (dragCat.current && cat) dropCatOnCat(dragCat.current, cat.id);
                      }}
                    >
                      {/* The header bar toggles the group's fold on click; interactive
                          children (grip, checkbox, name, buttons) stop propagation so they
                          keep their own behaviour. */}
                      <MemoCatHead
                        className={"memo-cat-head" + (collapsed ? " collapsed" : "")}
                        onClick={() => toggleCollapse(dropKey)}
                        // The head is the drag handle for reordering categories (the uncategorised bucket has none).
                        // Not while renaming: a draggable ancestor stops the name field from selecting text.
                        draggable={!!cat && renameCat !== cat.id}
                        onDragStart={cat ? (e) => {
                          if ((e.target as HTMLElement).closest("input")) return;
                          dragCat.current = cat.id;
                        } : undefined}
                        onDragEnd={cat ? () => (dragCat.current = null) : undefined}
                        onContextMenu={cat ? (e) => {
                          e.preventDefault();
                          e.stopPropagation();
                          setCatMenu({ at: { x: e.clientX, y: e.clientY }, catId: cat.id });
                        } : undefined}
                        onKeyDown={cat ? (e) => {
                          if (e.key !== "ContextMenu" && !(e.shiftKey && e.key === "F10")) return;
                          e.preventDefault();
                          const r = (e.currentTarget.querySelector(".memo-cat-more") ?? e.currentTarget).getBoundingClientRect();
                          setCatMenu({ at: { x: r.left, y: r.bottom + 2, anchor: r }, catId: cat.id });
                        } : undefined}
                      >
                        <button
                          type="button"
                          className="memo-cat-caret"
                          aria-expanded={!collapsed}
                          aria-label={tr(collapsed ? "memo.expand_category" : "memo.collapse_category")}
                          onClick={(e) => {
                            e.stopPropagation();
                            toggleCollapse(dropKey);
                          }}
                        >
                          <Icon name={collapsed ? "chevron-right" : "chevron-down"} />
                        </button>
                        <input
                          type="checkbox"
                          className="memo-cat-check"
                          checked={allSel}
                          disabled={ids.length === 0}
                          onClick={(e) => e.stopPropagation()}
                          onChange={() =>
                            setSel((s) => {
                              const n = { ...s };
                              // Clearing also drops folded rows that were selected while the fold was open.
                              const target = allSel ? g.memos.filter((m) => !isSuperseded(m)).map((m) => m.id) : ids;
                              for (const id of target) n[id] = !allSel;
                              return n;
                            })
                          }
                        />
                        {cat && renameCat === cat.id ? (
                          <input
                            className="memo-cat-rename"
                            defaultValue={cat.name}
                            autoFocus
                            onClick={(e) => e.stopPropagation()}
                            onBlur={(e) => void commitRename(cat, e.target.value)}
                            onKeyDown={(e) => {
                              if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                              else if (e.key === "Escape") setRenameCat(null);
                            }}
                          />
                        ) : (
                          <span
                            className="memo-cat-name"
                            title={cat ? tr("memo.rename_category") : undefined}
                            onClick={(e) => {
                              // A named category renames on click; the uncategorized bucket
                              // has no rename, so its name click just toggles the fold.
                              e.stopPropagation();
                              if (cat) setRenameCat(cat.id);
                              else toggleCollapse(dropKey);
                            }}
                          >
                            {catLabel(g.category)}
                          </span>
                        )}
                        <span className="memo-cat-n">{isQueue ? g.memos.filter(isWaiting).length : g.memos.length}</span>
                        {isQueue && (() => {
                          // One badge for the three states; the words stay as its accessible name when the rail is
                          // narrow enough to show the icon alone.
                          const b = queue?.paused
                            ? { cls: " paused", icon: "debug-pause", label: tr("memo.autorun_badge_paused"), title: tr("memo.autorun_badge_paused_title") }
                            : queue && !queue.enabled
                              ? { cls: " off", icon: "circle-slash", label: tr("memo.autorun_badge_off"), title: tr("memo.autorun_badge_off_title") }
                              : { cls: "", icon: "play-circle", label: tr("memo.autorun_badge"), title: tr("memo.autorun_badge_title") };
                          return (
                            <span className={"memo-q-badge" + b.cls} title={b.title} aria-label={b.label}>
                              <Icon name={b.icon} />
                              <span className="memo-q-badge-txt">{b.label}</span>
                            </span>
                          );
                        })()}
                        {!cat && ids.length > 0 && (
                          <button
                            type="button"
                            className="memo-cat-send linkish sm"
                            onClick={(e) => {
                              e.stopPropagation();
                              sendGroup(ids);
                            }}
                          >
                            {tr("common.send")}
                          </button>
                        )}
                        <button
                          type="button"
                          className="memo-cat-add"
                          title={tr("memo.add_to_category", { name: catLabel(g.category) })}
                          aria-label={tr("memo.add_to_category", { name: catLabel(g.category) })}
                          onClick={(e) => {
                            e.stopPropagation();
                            openComposerFor(rb.repo, g.category);
                          }}
                        >
                          <Icon name="add" />
                        </button>
                        {cat && (
                          <button
                            type="button"
                            className="memo-cat-more"
                            title={tr("memo.cat_menu_btn")}
                            aria-label={tr("memo.cat_menu_btn")}
                            aria-haspopup="menu"
                            onClick={(e) => {
                              e.stopPropagation();
                              const r = e.currentTarget.getBoundingClientRect();
                              setCatMenu({ at: { x: r.left, y: r.bottom + 2, anchor: r }, catId: cat.id });
                            }}
                          >
                            <Icon name="ellipsis" />
                          </button>
                        )}
                      </MemoCatHead>

                      {queue && <MemoQueueNotices queue={queue} busy={resuming} onResume={() => void resume(queue.categoryId)} />}

                      {cat && isQueue && !collapsed && (
                        <>
                          <button
                            type="button"
                            className="memo-runs-toggle"
                            aria-expanded={!!runsOpen[cat.id]}
                            onClick={() => setRunsOpen((o) => ({ ...o, [cat.id]: !o[cat.id] }))}
                          >
                            <Icon name={runsOpen[cat.id] ? "chevron-down" : "chevron-right"} />
                            {tr("memo.d_runs_toggle")}
                          </button>
                          {runsOpen[cat.id] && <MemoQueueRuns categoryId={cat.id} onShowAll={() => setDeliveriesFor(cat.id)} />}
                        </>
                      )}

                      {targetKey === dropKey && composer}

                      {cards.map((c) => (
                        <MemoReleaseCard
                          key={c.key}
                          card={c}
                          continuationMax={queue?.continuationMax}
                          busy={releasing === dropKey + "\x00" + c.key}
                          changedIds={changedIds}
                          edited={edited}
                          awaiting={awaiting}
                          onConfirm={confirmChange}
                          onRelease={(items) => void release(dropKey + "\x00" + c.key, items)}
                        />
                      ))}

                      {!collapsed && (
                        <div className="memo-cat-items">
                          {shown.map((m) => renderRow(m))}
                          {folded.length > 0 && (
                            <button
                              type="button"
                              className="memo-sent-fold"
                              aria-expanded={sentOpen}
                              onClick={() => setSentOpenCats((s) => ({ ...s, [dropKey]: !s[dropKey] }))}
                            >
                              <Icon name={sentOpen ? "chevron-down" : "chevron-right"} /> {tr("memo.sent_fold", { count: folded.length })}
                            </button>
                          )}
                          {history.map((m) => renderRow(m, true))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            ))}

            {addCatRow}
          </>
        )}

        {selectedIds.length > 0 && (
          <div className="memo-selbar">
            <span className="memo-selbar-n">{tr("memo.selected_n", { count: selectedIds.length })}</span>
            <Button small variant="ghost" className="memo-selbar-clear" onClick={() => setSel({})}>
              {tr("memo.clear_selection")}
            </Button>
            <Button small variant="primary" onClick={() => setSendOpen(true)}>
              {tr("memo.open_send")}
            </Button>
          </div>
        )}

        {catMenu && (() => {
          const c = cats.find((x) => x.id === catMenu.catId);
          if (!c) return null;
          const sendIds = catSendIds.current.get(c.id) ?? [];
          const q = queueByCat.get(c.id);
          const isQ = !!q || !!c.queue;
          // A category that already is a queue stays openable while the tenant blocks new setups: the
          // dialog still lets the member remove it. Until a settings read has succeeded the dialog would
          // edit a draft built from defaults, and a save would overwrite an existing queue's setting.
          const note = !tenantKnown ? tr("memo.q_unread") : tenantKnown && !tenantEnabled && !isQ ? tr("memo.q_tenant_off") : "";
          return (
            <CatHeadMenu
              at={catMenu.at}
              categoryId={c.id}
              isQueue={isQ}
              queue={q}
              sendCount={sendIds.length}
              settingsNote={note}
              onClose={() => setCatMenu(null)}
              onSend={() => sendGroup(sendIds)}
              onRunResult={(r) => {
                if (r === "started") bumpMemos();
                // The refusal says a slot is taken; the history names who holds it.
                if (r === "no_slot") {
                  setSlotsFullFor(true);
                  setDeliveriesFor(c.id);
                }
              }}
              onHistory={() => setDeliveriesFor(c.id)}
              onSettings={() => setSettingsFor(c.id)}
              onRename={() => setRenameCat(c.id)}
              onDelete={() => void deleteCategory(c)}
            />
          );
        })()}

        {memoMenu &&
          createPortal(
            <ul className="ui-menu" ref={memoMenuRef} style={{ left: memoMenu.x, top: memoMenu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
              {/* An expanded long memo can be taller than the pane, which puts its own
                  collapse control off screen; the row's ⋯ stays at the top of the row. */}
              {expanded[memoMenu.m.id] && (
                <li>
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => runMemoMenu(() => setExpanded((s) => ({ ...s, [memoMenu.m.id]: false })))}
                  >
                    <Icon name="fold" /> {tr("memo.collapse_hint")}
                  </button>
                </li>
              )}
              {/* A replaced note is read-only: the Control Plane refuses an edit and a send of it. */}
              {!isSuperseded(memoMenu.m) && (
                <>
                  <li>
                    <button type="button" className="ui-menu-item" onClick={() => runMemoMenu(() => setEditing(memoMenu.m.id))}>
                      <Icon name="edit" /> {tr("memo.edit")}
                    </button>
                  </li>
                  <li>
                    <button type="button" className="ui-menu-item" onClick={() => runMemoMenu(() => sendGroup([memoMenu.m.id]))}>
                      <Icon name="send" /> {tr("memo.send_one")}
                    </button>
                  </li>
                </>
              )}
              {canStartFromMemo(memoMenu.m) && (
                <li>
                  <button type="button" className="ui-menu-item" onClick={() => {
                      const m = memoMenu.m;
                      runMemoMenu(() =>
                        void startFromMemo(m).then((r) => {
                          if (!r.ok) toast(t(r.reason === "changed" ? "memo.start_draft_changed" : "memo.start_draft_failed"));
                          else if (r.saved) toast(t("memo.start_draft_saved"), { kind: "success" });
                        }),
                      );
                    }}>
                    <Icon name="rocket" /> {tr("memo.start_from")}
                  </button>
                </li>
              )}
              <li>
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() =>
                    runMemoMenu(() =>
                      void copyText(memoMenu.m.body).then((ok) => toast(t(ok ? "memo.copied" : "common.copy_failed"), ok ? { kind: "success" } : undefined)),
                    )
                  }
                >
                  <Icon name="copy" /> {tr("memo.copy")}
                </button>
              </li>
              <li className="ui-menu-sep" aria-hidden="true" />
              <li>
                <button type="button" className="ui-menu-item danger" onClick={() => runMemoMenu(() => void remove(memoMenu.m.id))}>
                  <Icon name="trash" /> {tr("common.delete")}
                </button>
              </li>
            </ul>,
            document.body,
          )}
      </Section>

      {settingsFor && (() => {
        const c = cats.find((x) => x.id === settingsFor);
        if (!c) return null;
        return (
          <MemoQueueSettingsModal
            key={c.id}
            categoryId={c.id}
            categoryName={c.name}
            queue={queueByCat.get(c.id)}
            tenantEnabled={tenantEnabled}
            destinations={queues
              .filter((q) => q.categoryId !== c.id)
              .map((q) => ({ id: q.categoryId, label: q.repo ? q.repo + " / " + q.category : q.category }))}
            onClose={() => setSettingsFor(null)}
            onChanged={bumpMemos}
          />
        );
      })()}
      {deliveriesFor && (() => {
        const c = cats.find((x) => x.id === deliveriesFor);
        if (!c) return null;
        return (
          <MemoQueueDeliveriesModal
            key={c.id}
            categoryId={c.id}
            categoryName={c.name}
            queue={queueByCat.get(c.id)}
            waiting={pendingCount(memos.filter((m) => m.repo === c.repo && m.category === c.name))}
            focusRun={focusRun}
            slotsFull={slotsFullFor}
            onClose={() => {
              setDeliveriesFor(null);
              setSlotsFullFor(false);
              setFocusRun("");
            }}
            onFired={bumpMemos}
            onUndone={bumpMemos}
            onResume={() => resume(c.id)}
          />
        );
      })()}
      {tidy && (
        <MemoTidyModal
          memos={tidy}
          categories={cats}
          allMemos={memos}
          onReload={bumpMemos}
          onClose={() => setTidy(null)}
          onDone={() => {
            setTidy(null);
            bumpMemos();
          }}
        />
      )}
      {sendOpen && selectedMemos.length > 0 && (
        <SendMemoModal
          memos={selectedMemos}
          onClose={() => setSendOpen(false)}
          onSent={() => {
            setSel({});
            setSendOpen(false);
            bumpMemos();
          }}
        />
      )}
    </>
  );
});

/** Counts toward an auto-run category's number: a memo still waiting, not started, delivered or replaced. */
const isWaiting = (m: Memo): boolean => !["claimed", "delivered", "unconfirmed", "superseded"].includes(m.state ?? "");

/** Category head. Publishes its height on `.memo-cat` as `--memo-cat-head-h` so the open fold line pins
 *  directly below it however tall the head grows (larger text, wrapping). */
function MemoCatHead(props: ComponentProps<"div">) {
  const ref = usePublishedHeight<HTMLDivElement>("--memo-cat-head-h");
  return <div {...props} ref={ref} />;
}

/** Repo row of the queue. Publishes its height on `.memo-repo` as `--memo-repo-head-h` so the
 *  pinned category rows (memo.css) sit directly below it, wrapped or not. Rendered only for a
 *  named repo; without it the variable stays unset and the offset falls back to 0. */
function MemoRepoHead({ repo, label, onAdd }: { repo: string; label: string; onAdd: () => void }) {
  const tr = useT();
  const ref = usePublishedHeight<HTMLDivElement>("--memo-repo-head-h");
  return (
    <div className="memo-repo-head" ref={ref}>
      <Icon name="repo" className="memo-repo-ic" />
      <span className="memo-repo-name">{label}</span>
      <button
        type="button"
        className="memo-repo-add"
        title={tr("memo.add_category_to_repo", { repo: label })}
        aria-label={tr("memo.add_category_to_repo", { repo: label })}
        onClick={onAdd}
      >
        <Icon name="add" />
      </button>
    </div>
  );
}

function dedupeGroups(groups: { repo: string; category: string }[]): { repo: string; category: string }[] {
  const seen = new Set<string>();
  return groups.filter((g) => {
    const k = g.repo + "\x00" + g.category;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

// One memo row: collapsed to 1 line, click to expand full text, click again to edit.
interface MemoRowProps {
  memo: Memo;
  selected: boolean;
  expanded: boolean;
  editing: boolean;
  currentCat: string;
  onToggle: () => void;
  onExpand: () => void;
  onCollapse: () => void;
  onEdit: () => void;
  onCancelEdit: () => void;
  /** after is the notes it waits for when the member changed them, absent otherwise. */
  /** after and startAfter are sent only when the member changed them (absent keeps them). */
  onSave: (body: string, category: string, clearRef: boolean, after?: string[], startAfter?: string) => void;
  /** The notes of its auto-run category it may wait for; absent outside one, or once it has run. */
  afterChoices?: { id: string; label: string }[];
  /** Opens the run history of this note's category; absent outside an auto-run category. */
  onOpenTidyRuns?: () => void;
  /** The category's follow-up allowance, for "follow-up K of M". */
  continuationMax?: number;
  /** Stops the chain a follow-up belongs to; absent on a note that is in none. */
  onStopChain?: () => void;
  onOpenMenu: (e: RMouseEvent) => void;
  onOpenKebab: (e: RMouseEvent) => void;
  onDragStart: (e: RDragEvent) => void;
  onDragEnd: () => void;
  onDropBefore: () => void;
}

/** Sort key of a history note: sent time, else created time; an unparsable stamp sorts last. */
const histTime = (m: Memo): number => {
  const t = Date.parse(m.sentAt || m.createdAt);
  return Number.isFinite(t) ? t : -Infinity;
};

function MemoRow(props: MemoRowProps) {
  const { memo: m, selected, expanded, editing, currentCat } = props;
  const tr = useT();
  const settings = useSettings();
  const [dragOver, setDragOver] = useState(false);
  const [body, setBody] = useState(m.body);
  const [cat, setCat] = useState(currentCat);
  // The member asked to drop the note's reference; it is sent with the save, not before.
  const [clearRef, setClearRef] = useState(false);
  const [after, setAfter] = useState<string[]>(m.after ?? []);
  // The start time as the datetime-local input holds it, a wall time of the member's time zone, and
  // what it held when the edit began: a list refresh that brings a time set elsewhere must not read
  // as the member clearing it.
  // The zone is fixed when the edit begins, so a change of the setting meanwhile cannot move the
  // instant the field shows.
  const [startZone, setStartZone] = useState(effectiveTimeZone);
  const [start, setStart] = useState(() => startAfterInputValue(m.startAfter, startZone));
  const [startBase, setStartBase] = useState(start);
  const [startBad, setStartBad] = useState(false);
  const ref = memoReference(m);
  // The earlier notes on offer, then any it already waits for that are no longer on offer (deleted,
  // moved, replaced, or kept past the list's window): those must stay visible so they can be removed.
  const afterOptions = props.afterChoices && [
    ...props.afterChoices.map((c) => ({ ...c, why: undefined as MsgKey | undefined })),
    ...(m.after ?? [])
      .filter((id) => !props.afterChoices!.some((c) => c.id === id))
      .map((id) => {
        const st = m.afterStatus?.find((a) => a.id === id);
        return { id, label: st?.label || id, why: st ? afterItemWords(st) : undefined };
      }),
  ];
  // Sent only when it differs from what is stored, so a plain text edit never touches it.
  const afterChanged = (): string[] | undefined => {
    const was = m.after ?? [];
    return after.length === was.length && after.every((id, i) => id === was[i]) ? undefined : after;
  };
  // Sent only when the member changed the field, so an untouched one never re-sends a stored time
  // (which may have passed, and the Control Plane would then clear it) nor clears one set elsewhere.
  const save = () => {
    const startAfter = start === startBase ? undefined : startAfterFromInput(start, startZone);
    if (startAfter === null) {
      setStartBad(true);
      return;
    }
    props.onSave(body, cat, clearRef, afterChanged(), startAfter);
  };
  const taRef = useRef<HTMLTextAreaElement>(null);
  useAutosizeTextarea(taRef, body);

  useEffect(() => {
    if (editing) {
      setBody(m.body);
      setCat(currentCat);
      setClearRef(false);
      setAfter(m.after ?? []);
      requestAnimationFrame(() => {
        const ta = taRef.current;
        if (ta) {
          ta.focus();
          ta.setSelectionRange(ta.value.length, ta.value.length);
        }
      });
    }
  }, [editing, m.body, currentCat]);
  // The start time's draft and zone are taken once, when the edit begins: a refresh of the note
  // while it is open (its text changed elsewhere) must not drop what the member typed, nor move it
  // to another zone. The time it brings is not lost: the field is sent only when changed.
  useEffect(() => {
    if (!editing) return;
    const zone = effectiveTimeZone();
    setStartZone(zone);
    setStart(startAfterInputValue(m.startAfter, zone));
    setStartBase(startAfterInputValue(m.startAfter, zone));
    setStartBad(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only the start of an edit resets it
  }, [editing]);

  if (editing) {
    return (
      <div className="memo-row editing">
        <div className="memo-edit">
          {ref && (
            <div className="memo-edit-ref">
              {clearRef ? (
                <div className="memo-ref-removing">
                  <span>{tr("memo.ref_remove_pending", { key: ref.key })}</span>
                  <Button small variant="ghost" onClick={() => setClearRef(false)}>
                    {tr("memo.ref_keep")}
                  </Button>
                </div>
              ) : (
                <>
                  <MemoReferenceLink reference={ref} />
                  <Button small variant="ghost" onClick={() => setClearRef(true)}>
                    {tr("memo.ref_remove")}
                  </Button>
                </>
              )}
            </div>
          )}
          <textarea
            ref={taRef}
            className="memo-edit-text"
            value={body}
            rows={3}
            onChange={(e) => setBody(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                props.onCancelEdit();
                return;
              }
              if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
              const mod = e.ctrlKey || e.metaKey;
              const submitWithKey = settings.mirrorSend !== "enter" ? mod : !e.shiftKey && !mod;
              if (submitWithKey) {
                e.preventDefault();
                save();
              }
            }}
          />
          <div className="memo-edit-row">
            <input
              className="memo-edit-cat"
              list="memo-cat-suggest"
              value={cat}
              placeholder={tr("memo.category_ph")}
              onChange={(e) => setCat(e.target.value)}
            />
            <Button small variant="primary" onClick={save}>
              {tr("memo.save")}
            </Button>
            <Button small variant="ghost" onClick={props.onCancelEdit}>
              {tr("common.cancel")}
            </Button>
          </div>
          {afterOptions && (
            <fieldset className="memo-edit-after">
              <legend>{tr("memo.after_edit_label")}</legend>
              {afterOptions.length === 0 ? (
                <div className="memo-edit-hint">{tr("memo.after_edit_none")}</div>
              ) : (
                <>
                  {afterOptions.map((c) => (
                    <label key={c.id} className="memo-edit-after-item">
                      <input
                        type="checkbox"
                        checked={after.includes(c.id)}
                        onChange={(e) =>
                          // Kept in the order shown, whatever order they were ticked in; a ticked one
                          // that is not offered any more stays until it is unticked.
                          setAfter((cur) =>
                            e.target.checked
                              ? afterOptions.map((x) => x.id).filter((id) => id === c.id || cur.includes(id))
                              : cur.filter((id) => id !== c.id),
                          )
                        }
                      />
                      <span>{c.label}</span>
                      {c.why && <span className="memo-edit-after-why">{tr(c.why)}</span>}
                    </label>
                  ))}
                  <div className="memo-edit-hint">{tr("memo.after_edit_hint")}</div>
                </>
              )}
            </fieldset>
          )}
          {/* Offered where the prerequisites are: a note of a queue that is held or waiting. */}
          {afterOptions && (
            <fieldset className="memo-edit-start">
              <legend>{tr("memo.start_edit_label")}</legend>
              <div className="memo-edit-start-row">
                <input
                  type="datetime-local"
                  className="memo-edit-start-input"
                  aria-label={tr("memo.start_edit_label")}
                  value={start}
                  onChange={(e) => {
                    setStart(e.target.value);
                    setStartBad(false);
                  }}
                />
                {start && (
                  <Button
                    small
                    variant="ghost"
                    onClick={() => {
                      setStart("");
                      setStartBad(false);
                    }}
                  >
                    {tr("memo.start_clear")}
                  </Button>
                )}
              </div>
              {startBad && (
                <div className="memo-edit-after-why" role="alert">
                  {tr("memo.start_invalid")}
                </div>
              )}
              <div className="memo-edit-hint">{tr("memo.start_edit_hint", { zone: startZone })}</div>
            </fieldset>
          )}
          <div className="memo-edit-hint">{m.kind === "file" ? tr("memo.edit_hint_file") : tr("memo.edit_hint")}</div>
        </div>
      </div>
    );
  }

  const stateWords = memoStateWords(m.state);
  const originWords = memoOriginWords(m);
  const marks = tidyMarks(m);
  const replaced = isSuperseded(m);
  const isLong = (m.body || "").length > 60 || (m.body || "").includes("\n");
  const metaNode = (stateWords || originWords || marks.length > 0) && (
    <div className="memo-qmeta">
      {stateWords && (
        <span className={"memo-qstate s-" + m.state} title={tr(stateWords.title)}>
          {tr(stateWords.label)}
        </span>
      )}
      {originWords && (
        <span className="memo-qorigin">
          <MemoProducerText k={originWords.key} name={originWords.name ?? ""} link={m.provenance === "workspace" && !!originWords.name} />
        </span>
      )}
      {m.state === "pending" && m.releasedBy === "setting" && (
        <span className="memo-qorigin" title={tr("memo.accepted_by_setting_title")}>
          {tr("memo.accepted_by_setting")}
        </span>
      )}
      {m.releasedBy === "continuation" && (m.state === "pending" || m.state === "delivered") && (
        <span className="memo-qorigin" title={tr("memo.continuation_title")}>
          {(m.chainSeq ?? 0) === 0
            ? tr("memo.continuation_badge_plain")
            : (props.continuationMax ?? 0) >= m.chainSeq! && (props.continuationMax ?? 0) > 0
              ? tr("memo.continuation_badge_of", { seq: m.chainSeq!, max: props.continuationMax! })
              : tr("memo.continuation_badge", { seq: m.chainSeq! })}
        </span>
      )}
      {props.onStopChain && (m.state === "pending" || m.state === "delivered") && (
        <button
          type="button"
          className="linkish"
          title={tr("memo.continuation_stop_title")}
          onClick={(e) => {
            e.stopPropagation();
            props.onStopChain!();
          }}
        >
          {tr("memo.continuation_stop")}
        </button>
      )}
      {releasedByTidy(m) && (
        <span className="memo-qorigin" title={tr("memo.released_by_tidy_title")}>
          {tr("memo.released_by_tidy")}
        </span>
      )}
      {marks.map((k) => (
        <span key={k.key} className="memo-qorigin memo-qtidy" title={k.title ? tr(k.title) : undefined}>
          {tr(k.key, k.params)}
        </span>
      ))}
      {replaced && props.onOpenTidyRuns && (
        <button
          type="button"
          className="linkish memo-qtidy-link"
          title={tr("memo.tidy_replaced_title")}
          onClick={(e) => {
            e.stopPropagation();
            props.onOpenTidyRuns!();
          }}
        >
          {tr("memo.tidy_replaced_link")}
        </button>
      )}
    </div>
  );
  return (
    <div
      className={
        "memo-row" +
        (selected ? " sel" : "") +
        (m.sentAt ? " sent" : "") +
        (expanded ? " exp" : "") +
        (dragOver ? " dragover" : "")
      }
      onContextMenu={props.onOpenMenu}
      onDragOver={(e) => {
        e.preventDefault();
        setDragOver(true);
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        e.stopPropagation();
        setDragOver(false);
        props.onDropBefore();
      }}
    >
      <span
        className="memo-grip"
        draggable={!replaced}
        title={tr("memo.reorder_memo")}
        onDragStart={props.onDragStart}
        onDragEnd={props.onDragEnd}
      >
        <Icon name="gripper" />
      </span>
      <input type="checkbox" checked={selected && !replaced} disabled={replaced} onChange={props.onToggle} />
      <div
        className="memo-body"
        onClick={(e) => {
          if ((e.target as HTMLElement).closest("code")) return;
          if (!expanded) props.onExpand();
          else if (!replaced) props.onEdit();
        }}
      >
        {m.origin && (
          <span className="memo-origin" title={tr("inbox.memo_origin_title")}>
            <Icon name="inbox" /> {tr("inbox.memo_origin", { who: m.origin.sender })}
          </span>
        )}
        {/* A file note shows its path instead; the server flush ignores a reference on one, and the
            Console never attaches one. The edit view and the approval card still show it, so a
            reference that reached it through the token face is never hidden where it is approved. */}
        {ref && m.kind !== "file" && <MemoReferenceLink reference={ref} trailing={metaNode} inApp />}
        {m.kind === "file" ? (
          <>
            <code className="memo-ref">{m.refPath}</code>
            {m.body && (
              <div className="memo-comment memo-text" title={isLong && !expanded ? tr("memo.expand_memo") : undefined}>
                {m.body}
              </div>
            )}
          </>
        ) : (
          m.body && (
            <div className="memo-text" title={isLong && !expanded ? tr("memo.expand_memo") : undefined}>
              {m.body}
            </div>
          )
        )}
        {m.attachments && m.attachments.length > 0 && (
          <div className="memo-row-thumbs" onClick={(e) => e.stopPropagation()}>
            {m.attachments.map((a) => (
              <MemoImageThumb key={a.name} name={a.name} />
            ))}
          </div>
        )}
        {/* Without a reference line to ride on, state and producer stay below the body. */}
        {!(ref && m.kind !== "file") && metaNode}
        {m.state === "held" && m.heldReason?.reason === "deferred" && (
          <div className="memo-after-held memo-deferred" title={(m.deferReasons ?? []).map((r) => r.reason).join("\n") || undefined}>
            {tr("memo.defer_held", { count: m.deferCount ?? 0 })}
            {(m.deferReasons ?? []).map((r, i) => (
              <span key={i} className="memo-deferred-reason">
                {r.reason}
              </span>
            ))}
          </div>
        )}
        {scheduledUntil(m) && <div className="memo-scheduled">{tr("memo.start_until", { at: fmtStartAfter(scheduledUntil(m)!) })}</div>}
        {deferredUntil(m) && (
          <div className="memo-deferred" title={(m.deferReasons ?? []).at(-1)?.reason || undefined}>
            {tr("memo.defer_until", { at: fmtDateTime(deferredUntil(m)!) })}
            {(m.deferReasons ?? []).length > 0 && <span className="memo-deferred-reason">{(m.deferReasons ?? []).at(-1)!.reason}</span>}
          </div>
        )}
        {m.state === "held" && continuationHeldKey(m.heldReason?.reason) && (
          <div className="memo-after-held">{tr("memo.continuation_held", { why: tr(continuationHeldKey(m.heldReason?.reason)!) })}</div>
        )}
        {m.state === "held" && m.heldReason && m.heldReason.reason !== "deferred" && !continuationHeldKey(m.heldReason.reason) && (
          <div className="memo-after-held">
            {tr("memo.after_held", {
              label: m.afterStatus?.find((a) => a.id === m.heldReason!.after)?.label || m.heldReason.after,
              reason: tr(afterItemWords({ state: "failed", reason: m.heldReason.reason })),
            })}
          </div>
        )}
        {m.afterStatus && m.afterStatus.length > 0 && (
          <div className="memo-after" title={tr("memo.after_title")}>
            <span className="memo-after-head">{tr("memo.after_label")}</span>
            {m.afterStatus.map((a) => (
              <span key={a.id} className={"memo-after-item a-" + a.state}>
                {a.label || a.id}: {tr(afterItemMessage(a).key, afterItemMessage(a).params)}
              </span>
            ))}
          </div>
        )}
        {((m.sentAt && !stateWords) || (isLong && expanded)) && (
          <div className="memo-meta">
            {m.sentAt && !stateWords && <span className="memo-sent-tag">{tr("memo.sent_tag")}</span>}
            {isLong &&
              (expanded ? (
                // Expanding is a click on the body, but a second one edits — without this
                // the row has no way back and a long memo owns the pane for good.
                <button
                  type="button"
                  className="memo-less linkish"
                  title={tr("memo.collapse_memo")}
                  onClick={(e) => {
                    e.stopPropagation();
                    props.onCollapse();
                  }}
                >
                  <Icon name="fold" /> {tr("memo.collapse_hint")}
                </button>
              ) : null)}
          </div>
        )}
      </div>
      <button type="button" className="memo-menu-btn" title={tr("srow.menu")} aria-haspopup="menu" onClick={props.onOpenKebab}>
        <Icon name="ellipsis" />
      </button>
    </div>
  );
}
