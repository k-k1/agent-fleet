// MemoQueueSection (docs/log/21, UI刷新) — the left-pane memo queue. Notes accumulate per
// membership and sync across devices (Control-Plane persisted, no server push → refetch
// on mount / store bump + slow poll while mounted). The revamp:
//   - the composer is hidden by default; a header ＋ or leader Ctrl/⌘+K → M reveals it;
//   - a queued memo is editable in place (click to expand full text, click again to edit);
//   - categories are first-class (add empty, rename, delete) and everything reorders by
//     drag — memos within/between categories, and the categories themselves;
//   - "送信…" opens SendMemoModal to edit the concatenated text and pick a destination.
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent, RefObject } from "react";
import { createPortal } from "react-dom";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
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
} from "./api.ts";
import { useMemoStore } from "./store.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useDraft } from "../../lib/draft.ts";
import { useSettings } from "../../lib/settings.ts";
import type { Memo, MemoCategory, MemoAttachment } from "../../types/memo.ts";
import { MemoTidyModal } from "./MemoTidyModal.tsx";
import { SendMemoModal } from "./SendMemoModal.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { consumeShare } from "./share.ts";
import { setMemoDragData } from "./dnd.ts";

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

// Keep memo composition fields as compact as their content permits, while letting a
// longer note grow without the user having to drag the resize handle first.
function useAutosizeTextarea(ref: RefObject<HTMLTextAreaElement | null>, value: string) {
  useLayoutEffect(() => {
    const textarea = ref.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${textarea.scrollHeight}px`;
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
  const memosKey = useMemoStore((s) => s.tick);
  const bumpMemos = useMemoStore((s) => s.bump);
  const composeReq = useMemoStore((s) => s.composeReq);
  const toast = useToast();
  const askConfirm = useConfirm();
  const tr = useT();

  const [memos, setMemos] = useState<Memo[]>([]);
  const [cats, setCats] = useState<MemoCategory[]>([]);
  const [sel, setSel] = useState<Record<string, boolean>>({});
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
  const [editing, setEditing] = useState<string | null>(null);
  const [renameCat, setRenameCat] = useState<string | null>(null); // category id being renamed
  const [composerOpen, setComposerOpen] = useState(false);
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
  const [sendOpen, setSendOpen] = useState(false);
  // Section open/closed, owned here (controlled) so the leader command can force it open.
  const [open, setOpen] = useState(() => localStorage.getItem(SECTION_KEY) !== "0");
  const serRef = useRef("");
  const composerTextRef = useRef<HTMLTextAreaElement>(null);
  useAutosizeTextarea(composerTextRef, newText);

  // Refetch memos + categories on mount / bump / tenant switch, and poll while mounted.
  useEffect(() => {
    let alive = true;
    const load = () =>
      Promise.all([memoList(), memoCategoryList()])
        .then(([mList, cList]) => {
          if (!alive) return;
          const marr = Array.isArray(mList) ? mList : [];
          const carr = Array.isArray(cList) ? cList : [];
          const ser = JSON.stringify([marr, carr]);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setMemos(marr);
            setCats(carr);
          }
        })
        .catch(() => {});
    load();
    const id = setInterval(() => {
      if (!document.hidden) load(); // hidden tab: skip the tick (mobile data / battery)
    }, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [memosKey, tenant]);

  // Leader Ctrl/⌘+K → M (or any requestCompose) reveals + focuses the composer.
  useEffect(() => {
    if (composeReq === 0) return;
    setOpen(true);
    setComposerOpen(true);
    requestAnimationFrame(() => composerTextRef.current?.focus());
  }, [composeReq]);

  const setSectionOpen = (o: boolean) => {
    setOpen(o);
    localStorage.setItem(SECTION_KEY, o ? "1" : "0");
  };

  const blocks = useMemo(() => groupMemos(memos, cats), [memos, cats]);
  const unsent = useMemo(() => memos.filter((m) => !m.sentAt), [memos]);
  const catNames = useMemo(() => [...new Set(cats.map((c) => c.name))], [cats]);
  const selectedIds = useMemo(() => memos.filter((m) => sel[m.id]).map((m) => m.id), [memos, sel]);
  const selectedMemos = useMemo(() => memos.filter((m) => sel[m.id]), [memos, sel]);

  const toggle = (id: string) => setSel((s) => ({ ...s, [id]: !s[id] }));

  // ---- composer ----------------------------------------------------------------
  const openComposer = () => {
    setComposerOpen(true);
    requestAnimationFrame(() => composerTextRef.current?.focus());
  };

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
    void memoImageGC([...keep]).catch(() => {});
  };

  const addMemo = async () => {
    const body = newText.trim();
    if ((!body && newImages.length === 0) || busy) return;
    setBusy(true);
    try {
      const category = newCat.trim();
      const res = await memoCreate({
        kind: "text",
        body,
        category,
        ...(newImages.length ? { attachments: newImages } : {}),
      });
      if ((res as { error?: unknown }).error) {
        toast(t("memo.add_failed"));
        return;
      }
      // A brand-new category becomes first-class so it's reorderable straight away.
      if (category && !catNames.includes(category)) await memoCategoryCreate({ repo: "", name: category }).catch(() => {});
      setNewText("");
      setNewImages([]);
      bumpMemos();
    } catch {
      toast(t("memo.add_failed"));
    } finally {
      setBusy(false);
    }
  };

  // One-shot: a PWA share (Android 共有シート) launches the app at ?share=<id>. Pull the
  // shared text + images out of the SW stash, reveal the composer, and prefill it.
  useEffect(() => {
    let alive = true;
    void consumeShare().then((s) => {
      if (!alive || !s || (!s.text && s.files.length === 0)) return;
      setOpen(true);
      setComposerOpen(true);
      if (s.text) setNewText((prev) => (prev ? prev + "\n" + s.text : s.text));
      if (s.files.length) void attachImages(s.files);
      requestAnimationFrame(() => composerTextRef.current?.focus());
    });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ---- memo edit / delete ------------------------------------------------------
  const saveEdit = async (m: Memo, body: string, category: string) => {
    setEditing(null);
    try {
      const res = await memoUpdate(m.id, { body: body.trim(), category: category.trim() });
      // apiJSON はサーバエラーを {error} で解決する（例外にならない）— 偽の成功トーストを出さない。
      if ((res as { error?: unknown }).error) {
        toast(t("common.send_failed"));
        return;
      }
      if (category.trim() && !catNames.includes(category.trim()))
        await memoCategoryCreate({ repo: m.repo, name: category.trim() }).catch(() => {});
      bumpMemos();
      toast(t("memo.updated"), { kind: "success" });
    } catch {
      toast(t("common.send_failed"));
    }
  };
  const remove = async (id: string) => {
    try {
      const res = await memoDelete(id);
      // 失敗時に下の GC まで走らせない: keep 集合から当該メモの画像が抜けるため、削除に
      // 失敗して生き残ったメモの画像だけがコンテナから消えてしまう。
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
      runImageGC(memos.filter((m) => m.id !== id), newImages);
      bumpMemos();
    } catch {
      toast(t("common.delete_failed"));
    }
  };

  // ---- categories --------------------------------------------------------------
  const addCategory = async () => {
    try {
      const res = await memoCategoryCreate({ repo: "", name: t("memo.new_category") });
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
      // apiJSON はサーバエラーを {error} で解決する（例外にならない）— 失敗を黙って握り潰さない。
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
      // raw() は HTTP エラーでも resolve する — res.ok を見ないと偽の成功トーストになる。
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
      grp.forEach((m, i) => patches.push(memoUpdate(m.id, { position: i, category, repo })));
    }
    try {
      await Promise.all(patches);
    } finally {
      bumpMemos();
    }
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
    // state 配列内のオブジェクトを直接書き換えない — 新オブジェクトで差し替える。
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
    // state 配列内のオブジェクトを直接書き換えない — 新オブジェクトで差し替える。
    const placed = { ...moved, repo, category };
    arr.push(placed);
    setMemos(arr);
    if (category) toast(t("memo.moved", { cat: category }));
    void persistGroups(arr, dedupeGroups([from, { repo, category }]));
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
    // 楽観更新: prev をそのまま map すると要素の並びが変わらず no-op（refetch まで反映されない）。
    // この repo のカテゴリが占めるスロットへ、並べ替え後の repoCats を先頭から順に差し込む。
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
          setTidy(selectedMemos);
        }}
      />
    </>
  );

  return (
    <>
      <Section
        id="memos"
        title={tr("memo.title")}
        icon="checklist"
        count={unsent.length}
        actions={actions}
        open={open}
        onToggle={() => setSectionOpen(!open)}
      >
        {composerOpen && (
          <div
            className="memo-add"
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
        )}

        {memos.length === 0 && cats.length === 0 ? (
          <div className="pane-empty">{tr("memo.empty")}</div>
        ) : (
          <>
            {blocks.map((rb) => (
              <div key={rb.repo} className="memo-repo">
                {rb.repo && (
                  <div className="memo-repo-head">
                    <Icon name="repo" className="memo-repo-ic" />
                    {repoLabel(rb.repo)}
                  </div>
                )}
                {rb.groups.map((g) => {
                  const ids = g.memos.map((m) => m.id);
                  const allSel = ids.length > 0 && ids.every((id) => sel[id]);
                  const cat = g.catId ? cats.find((c) => c.id === g.catId) : undefined;
                  const dropKey = rb.repo + "\x00" + g.category;
                  const collapsed = !!collapsedCats[dropKey];
                  return (
                    <div
                      key={g.category || "\x00unfiled"}
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
                      <div
                        className={"memo-cat-head" + (collapsed ? " collapsed" : "")}
                        onClick={() => toggleCollapse(dropKey)}
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
                        {cat ? (
                          <span
                            className="memo-cat-grip"
                            draggable
                            title={tr("memo.reorder_category")}
                            onClick={(e) => e.stopPropagation()}
                            onDragStart={() => (dragCat.current = cat.id)}
                            onDragEnd={() => (dragCat.current = null)}
                          >
                            <Icon name="gripper" />
                          </span>
                        ) : (
                          // 未分類 isn't reorderable/deletable, but keep the grip column so its
                          // header aligns with the named categories below.
                          <span className="memo-cat-grip placeholder" aria-hidden="true" />
                        )}
                        <input
                          type="checkbox"
                          className="memo-cat-check"
                          checked={allSel}
                          disabled={ids.length === 0}
                          onClick={(e) => e.stopPropagation()}
                          onChange={() =>
                            setSel((s) => {
                              const n = { ...s };
                              for (const id of ids) n[id] = !allSel;
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
                        <span className="memo-cat-n">{g.memos.length}</span>
                        {ids.length > 0 && (
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
                        {cat && (
                          <button
                            type="button"
                            className="memo-cat-del"
                            title={tr("memo.delete_category")}
                            aria-label={tr("memo.delete_category")}
                            onClick={(e) => {
                              e.stopPropagation();
                              void deleteCategory(cat);
                            }}
                          >
                            <Icon name="trash" />
                          </button>
                        )}
                      </div>

                      {!collapsed && (
                        <div className="memo-cat-items">
                          {g.memos.map((m) => (
                            <MemoRow
                              key={m.id}
                              memo={m}
                              selected={!!sel[m.id]}
                              expanded={!!expanded[m.id]}
                              editing={editing === m.id}
                              currentCat={g.category}
                              onToggle={() => toggle(m.id)}
                              onExpand={() => setExpanded((s) => ({ ...s, [m.id]: true }))}
                              onEdit={() => setEditing(m.id)}
                              onCancelEdit={() => setEditing(null)}
                              onSave={(body, category) => void saveEdit(m, body, category)}
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
                                if (dragMemo.current) dropMemoOnMemo(dragMemo.current, m.id);
                              }}
                            />
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            ))}

            <button type="button" className="memo-addcat" onClick={() => void addCategory()}>
              <Icon name="add" /> {tr("memo.add_category")}
            </button>
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

        {memoMenu &&
          createPortal(
            <ul className="ui-menu" ref={memoMenuRef} style={{ left: memoMenu.x, top: memoMenu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
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

      {tidy && (
        <MemoTidyModal
          memos={tidy}
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

function dedupeGroups(groups: { repo: string; category: string }[]): { repo: string; category: string }[] {
  const seen = new Set<string>();
  return groups.filter((g) => {
    const k = g.repo + "\x00" + g.category;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

// One memo row: collapsed to 2 lines, click to expand full text, click again to edit.
interface MemoRowProps {
  memo: Memo;
  selected: boolean;
  expanded: boolean;
  editing: boolean;
  currentCat: string;
  onToggle: () => void;
  onExpand: () => void;
  onEdit: () => void;
  onCancelEdit: () => void;
  onSave: (body: string, category: string) => void;
  onOpenMenu: (e: RMouseEvent) => void;
  onOpenKebab: (e: RMouseEvent) => void;
  onDragStart: (e: RDragEvent) => void;
  onDragEnd: () => void;
  onDropBefore: () => void;
}

function MemoRow(props: MemoRowProps) {
  const { memo: m, selected, expanded, editing, currentCat } = props;
  const tr = useT();
  const settings = useSettings();
  const [dragOver, setDragOver] = useState(false);
  const [body, setBody] = useState(m.body);
  const [cat, setCat] = useState(currentCat);
  const taRef = useRef<HTMLTextAreaElement>(null);
  useAutosizeTextarea(taRef, body);

  useEffect(() => {
    if (editing) {
      setBody(m.body);
      setCat(currentCat);
      requestAnimationFrame(() => {
        const ta = taRef.current;
        if (ta) {
          ta.focus();
          ta.setSelectionRange(ta.value.length, ta.value.length);
        }
      });
    }
  }, [editing, m.body, currentCat]);

  if (editing) {
    return (
      <div className="memo-row editing">
        <div className="memo-edit">
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
                props.onSave(body, cat);
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
            <Button small variant="primary" onClick={() => props.onSave(body, cat)}>
              {tr("memo.save")}
            </Button>
            <Button small variant="ghost" onClick={props.onCancelEdit}>
              {tr("common.cancel")}
            </Button>
          </div>
          <div className="memo-edit-hint">{m.kind === "file" ? tr("memo.edit_hint_file") : tr("memo.edit_hint")}</div>
        </div>
      </div>
    );
  }

  const isLong = (m.body || "").length > 60 || (m.body || "").includes("\n");
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
        draggable
        title={tr("memo.reorder_memo")}
        onDragStart={props.onDragStart}
        onDragEnd={props.onDragEnd}
      >
        <Icon name="gripper" />
      </span>
      <input type="checkbox" checked={selected} onChange={props.onToggle} />
      <div
        className="memo-body"
        onClick={(e) => {
          if ((e.target as HTMLElement).closest("code")) return;
          if (!expanded) props.onExpand();
          else props.onEdit();
        }}
      >
        {m.kind === "file" ? (
          <>
            <code className="memo-ref">{m.refPath}</code>
            {m.body && <div className="memo-comment memo-text">{m.body}</div>}
          </>
        ) : (
          m.body && <div className="memo-text">{m.body}</div>
        )}
        {m.attachments && m.attachments.length > 0 && (
          <div className="memo-row-thumbs" onClick={(e) => e.stopPropagation()}>
            {m.attachments.map((a) => (
              <MemoImageThumb key={a.name} name={a.name} />
            ))}
          </div>
        )}
        {(m.sentAt || (!expanded && isLong)) && (
          <div className="memo-meta">
            {m.sentAt && <span className="memo-sent-tag">{tr("memo.sent_tag")}</span>}
            {!expanded && isLong && <span className="memo-more">{tr("memo.expand_hint")}</span>}
          </div>
        )}
      </div>
      <button type="button" className="memo-menu-btn" title={tr("srow.menu")} aria-haspopup="menu" onClick={props.onOpenKebab}>
        <Icon name="ellipsis" />
      </button>
    </div>
  );
}
