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
import { bindStudio, createStudio, studioPersona, type StudioDraft } from "./api.ts";
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
  /** "Switch agents": the session the studio is bound to now. Unbound before the create, because
   *  the Agent refuses to take a studio from a session that is still alive (409 studio_bound). */
  replacing?: string;
}): Promise<AttachResult> {
  let studioId = p.studioId;
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
    // /halt, not /stop: /stop forgets the session — its conversation leaves the list for good —
    // while switching agents only ends this one's turn at the studio. Halted, it stays resumable
    // (the Console's own stop button is the same call).
    void raw(`api/sessions/${encodeURIComponent(p.replacing)}/halt`, { method: "POST" }).catch(() => undefined);
  }
  if (!studioId) {
    try {
      const s = await createStudio({ draft: p.draft(), ...(p.title ? { title: p.title } : {}) });
      if (!s || s.error || !s.id) return { studioId: null, error: (s?.error && errText(s.error)) || t("imggen.studio_create_failed") };
      studioId = s.id;
    } catch {
      return { studioId: null, error: t("imggen.studio_create_failed") };
    }
  }
  let persona = "";
  try {
    const r = await studioPersona(studioId);
    if (!r || r.error || !r.prompt) return { studioId, error: (r?.error && errText(r.error)) || t("imggen.persona_failed") };
    persona = r.prompt;
  } catch {
    return { studioId, error: t("imggen.persona_failed") };
  }
  const o = p.opts;
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
  return { studioId, session: res.name as string };
}
