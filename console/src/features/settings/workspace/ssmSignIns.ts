// Shared IAM Identity Center sign-ins (ADR 0125): the type GET /api/ssm/sign-ins lists, bound to
// the Control Plane's ssmSignInDTO by the contract test, and what the Settings UI needs to name
// and save one.
import type { MsgKey } from "../../../lib/i18n/index.ts";

/** One shared sign-in, as GET /api/ssm/sign-ins lists it. */
export interface SsmSignIn {
  id: string;
  name: string;
  startUrl: string;
  ssoRegion: string;
  createdAt: string;
  /** The ids of the profiles in it. */
  profiles: string[];
}

/** The Control Plane's rule for a sign-in name (store.SignInNameRe): it becomes the sso-session
 *  af-<name>, which has to fit 64 characters of the CLI's allowlist. */
export const SIGN_IN_NAME_RE = /^[A-Za-z0-9._@-]{1,61}$/;

// The refusals of a profile or sign-in save that need the member to change something specific;
// anything else shows the server's own text.
const SAVE_ERRORS: Record<string, MsgKey> = {
  sign_in_mismatch: "ssm.err_sign_in_mismatch",
  sign_in_name_taken: "ssm.err_sign_in_name_taken",
  sign_in_exists: "ssm.err_sign_in_exists",
  bad_sign_in: "ssm.err_bad_sign_in",
};

/** The message for a sign-in refusal code, or null for any other answer. */
export function signInSaveError(code: unknown): MsgKey | null {
  return typeof code === "string" && Object.hasOwn(SAVE_ERRORS, code) ? SAVE_ERRORS[code] : null;
}
