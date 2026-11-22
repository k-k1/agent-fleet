// personalApi — typed wrappers for the endpoints the personal settings tabs call (account login
// methods, user instructions, the skills catalogue and the AI-assist resolution).
// The paths stay string literals (control-plane/console_routes_test.go reads them as text), except
// the skill-file URL, which SkillsTab.skillFileUrl builds and getSkillFile only sends.
//
// api() can resolve with a JSON `null` body. Every wrapper keeps `| null` in its type and the
// caller keeps the null handling it had; where it dereferenced a reply it stored, it calls
// `assertBody` (workspaceApi.ts) right after the `error` test.
import { api, apiJSON } from "../../../core/api/client.ts";
import type { Rep } from "../workspace/workspaceApi.ts";
import type { LinkableMethod, LinkedMethod } from "./AccountTab.tsx";
import type { Payload } from "./InstructionsTab.tsx";
import type { AiAssistResolutionRow } from "./aiAssistResolution.ts";
import type { CatalogKind } from "./SkillsTab.tsx";

export type LoginMethods = { enabled?: boolean; linked?: LinkedMethod[]; linkable?: LinkableMethod[] };
export const getLoginMethods = (): Promise<Rep<LoginMethods> | null> => api("api/me/login-methods");
/** `q` is the already-encoded `provider=…&subject=…` query. */
export const detachLoginMethod = (q: string): Promise<Rep<{ ok?: boolean }> | null> =>
  apiJSON("api/me/login-methods?" + q, "DELETE");

export const getUserNotes = (): Promise<Rep<Payload> | null> => api("api/user-notes");
export const putUserNotes = (body: Record<string, unknown>): Promise<Rep<Payload> | null> =>
  apiJSON("api/user-notes", "PUT", body);
export type NotePreview = { path: string; content: string };
export const getUserNotePreview = (kind: string): Promise<Rep<NotePreview> | null> =>
  api("api/user-notes/preview?kind=" + encodeURIComponent(kind));

export const getSkillsCatalog = (): Promise<Rep<{ kinds?: CatalogKind[] }> | null> => api("api/skills/catalog");
/** `url` comes from `skillFileUrl`; `content` is checked by the caller. */
export const getSkillFile = (url: string): Promise<Rep<{ content?: string }> | null> => api(url);

/** An error body has no `features`, and the caller treats that as an empty list. */
export const getAiAssistResolution = (): Promise<{ features?: AiAssistResolutionRow[] } | null> =>
  api("api/ai-assist/resolution");
