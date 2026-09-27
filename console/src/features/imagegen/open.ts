// openImagegen — the one way in to the image-generation pane (ADR 0081 decision 6).
//
// Its own module, apart from the view, for the reason `overview/open.ts` states: the
// keyboard command table and the ops bar import this, and pulling the view in would drag
// the form, its CSS and the fs client into every bundle that has a menu. It must also never
// reach the terminal service — `sessions/open.ts` does, and importing THAT from the command
// table is what broke the node test project, which has no window for xterm to attach to.
//
// Every pane edits a studio (ADR 0100 decision 10, revised): a draft kept in this browser's
// localStorage never reached the member's phone, and a studio needs no agent to exist
// (decision 2, step 1). So opening without a studio creates one. `sameTarget` dedupes on the
// studio id, so a second open of the same studio focuses the pane that has it.
import { useLayoutStore } from "../../layout/store.ts";
import { errText, getTenant } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { toast } from "../../ui/toast.ts";
import { createStudio, getStudio } from "./api.ts";
import { draftKey, emptyDraft, loadDraft, type ImagegenDraft } from "./draft.ts";
import { studioFromForm } from "./studioSync.ts";

export interface OpenImagegenOptions {
  /**
   * Fields for a NEW studio ("open in image generation" from the lightbox's properties bar).
   * They become that studio's draft; the studio open elsewhere is left as it is.
   */
  draft?: ImagegenDraft;
  /** Ctrl/⌘-click or middle click: open beside instead of in the current pane. */
  newPane?: boolean;
  /** The studio to open. Absent: the one this browser opened last, else a new one. */
  studioId?: string;
  /** Always a new, empty studio (the picker's "new studio", the command table). */
  fresh?: boolean;
}

/** Exactly one of the two is set. */
export interface OpenImagegenResult {
  studioId?: string;
  error?: string;
}

export function openImagegen(opts: OpenImagegenOptions = {}): Promise<OpenImagegenResult> {
  if (opts.studioId) {
    show(opts.studioId, !!opts.newPane);
    return Promise.resolve({ studioId: opts.studioId });
  }
  return once("open", async () => {
    let id = !opts.draft && !opts.fresh ? await rememberedStudio() : null;
    if (!id) {
      const r = await newStudio(opts.draft ?? emptyDraft());
      if (!r.studioId) {
        toast(r.error, { kind: "error" });
        return r;
      }
      id = r.studioId;
    }
    show(id, !!opts.newPane);
    return { studioId: id };
  });
}

function show(studioId: string, newPane: boolean): void {
  const st = useLayoutStore.getState();
  const target = { content: { kind: "imagegen" as const, studioId } };
  if (newPane) st.openTargetInNew(target);
  else st.openTarget(target);
}

// The remembered studio, unless the Agent says it is gone. Any other answer — a stopped
// workspace, a 502 while the Agent restarts — opens it anyway: the pane retries its first read,
// and creating a second studio because the first could not be reached is the worse mistake.
async function rememberedStudio(): Promise<string | null> {
  const id = lastStudio();
  if (!id) return null;
  try {
    const r = await getStudio(id);
    if (r?.error && /not_found|no_studio/.test(r.error.code || "")) {
      rememberStudio(null);
      return null;
    }
  } catch {
    /* unreachable is not "gone" */
  }
  return id;
}

/**
 * Create a studio holding `draft` and remember it as this browser's last. Does not open a pane
 * and does not toast — the pane that migrates itself shows the failure in place.
 */
export async function newStudio(draft: ImagegenDraft): Promise<OpenImagegenResult> {
  try {
    const s = await createStudio({ draft: studioFromForm(draft) });
    if (!s || s.error || !s.id) return { error: (s?.error && errText(s.error)) || t("imggen.studio_create_failed") };
    rememberStudio(s.id);
    return { studioId: s.id };
  } catch {
    return { error: t("imggen.studio_create_failed") };
  }
}

// One creation per key at a time: a double click, or React mounting a migrating pane twice,
// must not leave two studios behind. The second caller gets the first one's answer.
const inflight = new Map<string, Promise<OpenImagegenResult>>();

export function once(key: string, run: () => Promise<OpenImagegenResult>): Promise<OpenImagegenResult> {
  const cur = inflight.get(key);
  if (cur) return cur;
  const p = run().finally(() => inflight.delete(key));
  inflight.set(key, p);
  return p;
}

/**
 * The draft a pane from before the studio-only revision kept in this browser, or null when
 * there is none. Read once, by the pane that moves it into a studio; `dropLocalDraft` then
 * removes it so a second pane does not copy it again.
 */
export function localDraft(): ImagegenDraft | null {
  try {
    if (localStorage.getItem(draftKey(getTenant())) == null) return null;
  } catch {
    return null;
  }
  return loadDraft(draftKey(getTenant()));
}

export function dropLocalDraft(): void {
  try {
    localStorage.removeItem(draftKey(getTenant()));
  } catch {
    /* a blocked store has nothing to drop */
  }
}

// The one thing localStorage still remembers once studios exist (ADR 0100 decision 2): which
// studio this browser opened last. The draft itself is the Agent's.
const lastKey = (): string => `af.imagegen-last-studio.${getTenant() || "default"}`;

export function lastStudio(): string | null {
  try {
    return localStorage.getItem(lastKey()) || null;
  } catch {
    return null;
  }
}

export function rememberStudio(id: string | null): void {
  try {
    if (id) localStorage.setItem(lastKey(), id);
    else localStorage.removeItem(lastKey());
  } catch {
    /* a blocked store only forgets which studio was last */
  }
}
