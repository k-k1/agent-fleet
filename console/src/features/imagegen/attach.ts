// attach — "attach an agent" as three requests in a fixed order (ADR 0100 decision 2):
//
//   1. create the studio, moving the pane's localStorage draft into it (skipped when the pane
//      already has one);
//   2. read the persona — the first turn, composed by the Agent in the member's language;
//   3. create the session with `studio` and the persona as `initial_prompt`. The Agent writes
//      Meta.Studio and binds the studio BEFORE the first turn goes out, so that turn already
//      sees get_image_studio. The other order would start an agent that has no studio tools.
//
// A failure after step 1 leaves the studio unbound — the draft is not lost, and the pane opens
// it with the attach button still there.
import { apiJSON, errDetail, errText, raw } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { bindStudio, createStudio, getStudio, patchStudio, studioPersona, type StudioDraft, type StudioPatch } from "./api.ts";
import type { AttachOpts } from "./parts/AttachAgentModal.tsx";

export interface AttachResult {
  /** The studio, whether or not the session came up. */
  studioId: string | null;
  session?: string;
  error?: string;
}

export async function attachAgent(p: {
  studioId: string | null;
  /** The pane's draft, for step 1. */
  draft: () => StudioDraft;
  title?: string;
  opts: AttachOpts;
  /** The pane's studio as last read, when there is one: the dialog's model is written into it,
   *  and an untitled studio takes the default title. */
  existing?: { title: string; updatedAt: string; draft: StudioDraft };
  /** "Switch agents": the session the studio is bound to now. Unbound before the create, because
   *  the Agent refuses to take a studio from a session that is still alive (409 studio_bound). */
  replacing?: string;
}): Promise<AttachResult> {
  let studioId = p.studioId;
  // Whether this call gave the studio its default title (a new studio, or an untitled one), and so
  // may correct it once the session says where it really works.
  const defaulted = !p.title && (!p.studioId || (!!p.existing && !p.existing.title));
  if (p.replacing && studioId) {
    // Unbind first, and wait for it: the create refuses a studio still bound to a live session
    // (409 studio_bound), and a stop does not finish before this returns. Unbound, the old
    // session's tools are refused by the studio at once; stopping it is then only tidying.
    try {
      const u = await bindStudio(studioId, "");
      if (!u || u.error) return { studioId, error: (u?.error && errText(u.error)) || t("imggen.attach_failed") };
    } catch {
      return { studioId, error: t("err.network") };
    }
    // /halt, not /stop: /stop deletes the session (to the trash, ADR 0101) —
    // while switching agents only ends this one's turn at the studio. Halted, it stays resumable
    // (the Console's own stop button is the same call).
    void raw(`api/sessions/${encodeURIComponent(p.replacing)}/halt`, { method: "POST" }).catch(() => undefined);
  }
  const o = p.opts;
  const image: StudioDraft = { provider: o.imageProvider || undefined, model: o.imageModel || undefined };
  const title = p.title || defaultStudioTitle(o);
  if (!studioId) {
    try {
      const s = await createStudio({ draft: { ...p.draft(), ...image }, ...(title ? { title } : {}) });
      if (!s || s.error || !s.id) return { studioId: null, error: (s?.error && errText(s.error)) || t("imggen.studio_create_failed") };
      studioId = s.id;
    } catch {
      return { studioId: null, error: t("imggen.studio_create_failed") };
    }
  }
  if (p.existing && studioId) {
    const err = await writeChoice(studioId, p.existing, image, title);
    if (err) return { studioId, error: err };
  }
  let persona = "";
  try {
    const r = await studioPersona(studioId);
    if (!r || r.error || !r.prompt) return { studioId, error: (r?.error && errText(r.error)) || t("imggen.persona_failed") };
    persona = r.prompt;
  } catch {
    return { studioId, error: t("imggen.persona_failed") };
  }
  const body: Record<string, unknown> = { dir: o.dir, kind: o.kind, driver: o.driver, studio: studioId, initial_prompt: persona };
  if (o.model) body.model = o.model;
  if (o.effort) body.effort = o.effort;
  if (typeof o.skipPermissions === "boolean") body.skip_permissions = o.skipPermissions;
  if (o.subdir) body.subdir = o.subdir;
  if (o.worktree) {
    body.worktree = true;
    if (o.base) body.branch = o.base;
    // "" lets the Agent mint temp/<slug>, the same as the launch dialog's default.
    body.new_branch = "";
  }
  let res;
  try {
    res = await apiJSON("api/sessions", "POST", body);
  } catch {
    return { studioId, error: t("err.network") };
  }
  if (!res || res.error || !res.name) return { studioId, error: res?.error ? errDetail(res.error) : t("imggen.attach_failed") };
  // A new worktree is named by the Agent, so the default title above names the row the member
  // started from, and two studios cut from one row read the same in the picker. The create
  // answers with the working copy the agent actually got.
  const repo = typeof res.repo === "string" ? res.repo : "";
  if (o.worktree && defaulted && repo && repo !== o.place) {
    await retitle(studioId, title, defaultStudioTitle({ place: repo, kind: o.kind }));
  }
  return { studioId, session: res.name as string };
}

// Cosmetic, so a failure is dropped; and only while the studio still carries the title this attach
// wrote, so a name the member typed in the meantime is never replaced.
async function retitle(id: string, from: string, to: string): Promise<void> {
  try {
    const cur = await getStudio(id);
    if (!cur || cur.error || (cur.title || "") !== from) return;
    await patchStudio(id, { author: "human", title: to }, cur.updated_at);
  } catch {
    // The studio keeps the row's name.
  }
}

/** "<working copy> · <agent>" ("Home · <agent>" without one): what a studio is called until the
 *  member names it, so two studios in the picker are told apart by where their agent works. */
export function defaultStudioTitle(o: Pick<AttachOpts, "place" | "kind">): string {
  return `${o.place || t("imggen.start_title_home")} · ${kindDisplayName(o.kind)}`;
}

// The dialog's model into an existing studio (and the default title into an untitled one), before
// the persona is read: the first turn is written for that model. One retry on 412 — the pane's
// own debounced save may have moved the version since it was read.
async function writeChoice(
  id: string,
  existing: NonNullable<Parameters<typeof attachAgent>[0]["existing"]>,
  image: StudioDraft,
  title: string,
): Promise<string | null> {
  let cur = existing;
  for (let i = 0; i < 2; i++) {
    const draft: NonNullable<StudioPatch["draft"]> = {};
    if (image.model && (cur.draft.model || "") !== image.model) draft.model = image.model;
    if (image.provider && (cur.draft.provider || "") !== image.provider) draft.provider = image.provider;
    const body: StudioPatch = { author: "human" };
    if (Object.keys(draft).length) body.draft = draft;
    if (!cur.title && title) body.title = title;
    if (!body.draft && !body.title) return null;
    const r = await patchStudio(id, body, cur.updatedAt);
    if (r.status >= 200 && r.status < 300 && !r.error) return null;
    if (r.status !== 412 || i > 0) break;
    const fresh = await getStudio(id).catch(() => null);
    if (!fresh || fresh.error || !fresh.id) break;
    cur = { title: fresh.title || "", updatedAt: fresh.updated_at, draft: fresh.draft || {} };
  }
  // A title that did not land is cosmetic; an engine or model that did not land is not: model ids
  // overlap across fleet rows, so the same id on the old row would start on an engine nobody chose.
  const missed =
    (!!image.model && (cur.draft.model || "") !== image.model) || (!!image.provider && (cur.draft.provider || "") !== image.provider);
  return missed ? t("imggen.start_model_failed") : null;
}
