// backupApi — typed wrappers for the endpoints the settings export / import tab (BackupTab)
// reads and writes. The paths stay string literals here: control-plane/console_routes_test.go
// reads them as text to prove each one has a route.
//
// Replies follow api.ts: `T | { error }`, told apart by `isOk`.
import { api, rawJSON } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type { GcpProfileEntry } from "./settingsBundle.ts";

/** One SSM profile or host row as the export / import planners read it. Their field-by-field
 *  narrowing lives in settingsBundle.ts, which also serves the AWS import flows, so the row
 *  stays open here rather than duplicating the SSM tab's own wire type. */
export type BackupRow = Record<string, unknown>;

/** One Google Cloud profile (control-plane/gcp_profiles.go gcpProfileDTO). */
export type GcpProfileRow = {
  id: string;
  label: string;
  loginMethod: string;
  project: string;
  quotaProject: string;
  account: string;
  region: string;
  zone: string;
  impersonateServiceAccount: string;
  /** The gcloud configuration name the CP derives; ignored on input. */
  name: string;
  /** Present only when another label maps to the same name. */
  conflict?: { reason: string; labels: string[] };
};

/** One agent kind's row in the user-instructions answer (workspace/agent instrTarget). */
export type UserNotesTarget = {
  kind: string;
  supported: boolean;
  /** Why an unsupported kind is unsupported. */
  reason?: string;
  delivery?: string;
  path?: string;
  /** The member's choice; always false for an unsupported kind. */
  on: boolean;
  /** Whether the state on disk matches the choice right now. */
  applied: boolean;
  error?: string;
};

/** GET /api/user-notes (workspace/agent/agent_instructions.go instrStateWire). */
export type UserNotes = {
  text: string;
  bytes: number;
  max_bytes: number;
  enabled: boolean;
  path: string;
  targets: UserNotesTarget[];
  fleet_bytes: number;
};

/** PUT /api/user-notes: the Agent applies only the members that are present. Import sends all
 *  three, exactly as the bundle carries them. */
export type UserNotesPut = { text: string; enabled: boolean; targets: Record<string, boolean> };

export const listSsmProfileRows = (): Promise<Reply<BackupRow[]>> => api("api/ssm/profiles");
export const listSsmHostRows = (): Promise<Reply<BackupRow[]>> => api("api/ssm/hosts");
export const listGcpProfiles = (): Promise<Reply<GcpProfileRow[]>> => api("api/gcp/profiles");
export const getUserNotes = (): Promise<Reply<UserNotes>> => api("api/user-notes");

/** Returns the raw Response: the import reads `ok` / `status` and the error body itself. */
export const putUserNotes = (body: UserNotesPut): Promise<Response> => rawJSON("api/user-notes", "PUT", body);
/** Same: one POST per profile, each answered (and counted) on its own. */
export const createGcpProfile = (profile: GcpProfileEntry): Promise<Response> =>
  rawJSON("api/gcp/profiles", "POST", profile);
