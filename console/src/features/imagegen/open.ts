// openImagegen — the one way in to the image-generation pane (ADR 0081 decision 6).
//
// Its own module, apart from the view, for the reason `overview/open.ts` states: the
// keyboard command table and the ops bar import this, and pulling the view in would drag
// the form, its CSS and the fs client into every bundle that has a menu. It must also never
// reach the terminal service — `sessions/open.ts` does, and importing THAT from the command
// table is what broke the node test project, which has no window for xterm to attach to.
//
// `sameTarget` dedupes on the studio id (null matches null), so a second open of the same
// studio focuses the pane that has it (ADR 0100 decision 10).
import { useLayoutStore } from "../../layout/store.ts";
import { getTenant } from "../../core/api/client.ts";
import { draftKey, loadDraft, saveDraft, type ImagegenDraft } from "./draft.ts";

export interface OpenImagegenOptions {
  /**
   * Fields to load into the form before the pane appears ("open in image generation" from
   * the lightbox's properties bar). Written to the DRAFT, not to the pane content: the pane
   * has no fields of its own, and a 2 kB prompt does not belong in the layout store.
   */
  draft?: ImagegenDraft;
  /** Ctrl/⌘-click or middle click: open beside instead of in the current pane. */
  newPane?: boolean;
  /**
   * The studio to open (ADR 0100). Absent: the one this browser opened last, else the
   * studio-less pane. null: the studio-less pane explicitly — "open in image generation" writes
   * the localStorage draft, which only that pane reads.
   */
  studioId?: string | null;
}

export function openImagegen(opts: OpenImagegenOptions = {}): void {
  if (opts.draft) saveDraft(draftKey(getTenant()), opts.draft);
  const studioId = opts.studioId !== undefined ? opts.studioId : opts.draft ? null : lastStudio();
  const st = useLayoutStore.getState();
  const target = { content: { kind: "imagegen" as const, studioId } };
  if (opts.newPane) st.openTargetInNew(target);
  else st.openTarget(target);
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

/** The draft as it stands for this workspace — the base "open in image generation" lays a
 *  picture's recovered fields over, so untouched fields keep what the user had typed. */
export const currentDraft = (): ImagegenDraft => loadDraft(draftKey(getTenant()));
