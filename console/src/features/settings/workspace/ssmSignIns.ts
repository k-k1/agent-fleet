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

// --- Grouping existing profiles into one sign-in (ADR 0125 decision 6) ---------------------

/** The part of an ungrouped SSO profile the grouping reads (GET /api/ssm/profiles). */
export interface GroupCandidate {
  id: string;
  label: string;
  /** The workspace profile name: its own sign-in's cache key is af-<name>. */
  name: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
  confirmBeforeUse: boolean;
  nameCollides: boolean;
}

/** Only an SSO profile with a sign-in of its own can be grouped. */
export function groupable(p: any): boolean {
  return p?.kind !== "assume_role" && !p?.signIn;
}

export function toCandidate(p: any): GroupCandidate {
  const s = (v: unknown) => (v == null ? "" : String(v));
  return {
    id: s(p.id),
    label: s(p.label),
    name: s(p.name),
    startUrl: s(p.startUrl),
    ssoRegion: s(p.ssoRegion),
    accountId: s(p.accountId),
    roleName: s(p.roleName),
    region: s(p.region),
    confirmBeforeUse: p.confirmBeforeUse === true,
    nameCollides: p.nameCollides === true,
  };
}

/** True when every profile has the same start URL and SSO region, compared as the Control Plane
 *  does (exactly): a looser rule would pass a group the first write then refuses. */
export function samePortal(ps: GroupCandidate[]): boolean {
  return ps.length > 0 && ps.every((p) => p.startUrl === ps[0].startUrl && p.ssoRegion === ps[0].ssoRegion);
}

/** The name to propose: a member that is signed in now (its sso-session name does not change, so
 *  its token becomes the group's: decision 7 / D), else the first member's name. "" when no
 *  member's name is a valid sign-in name. */
export function proposeName(ps: GroupCandidate[], live: (profileName: string) => boolean): string {
  const ok = (p: GroupCandidate) => !p.nameCollides && SIGN_IN_NAME_RE.test(p.name);
  return (ps.find((p) => ok(p) && live(p.name)) ?? ps.find(ok))?.name ?? "";
}

/** The order to write the profiles in. The member whose name the sign-in takes goes first: until
 *  it has moved in, it is a profile with a sign-in of its own with the sign-in's name, which the
 *  Control Plane refuses (sign_in_name_taken). The rest keep their order. */
export function writeOrder(ps: GroupCandidate[], name: string): GroupCandidate[] {
  return [...ps.filter((p) => p.name === name), ...ps.filter((p) => p.name !== name)];
}

export type NameProblem = "empty" | "invalid" | "mismatch" | "taken";

/** What stops the chosen name before any write (null = none). `others` are the member's ungrouped
 *  SSO profiles outside the group: one of them named like the sign-in is the CP's name_taken. */
export function nameProblem(
  name: string,
  group: GroupCandidate[],
  others: { name: string }[],
  signIns: { name: string; startUrl: string; ssoRegion: string }[],
): NameProblem | null {
  if (!name) return "empty";
  if (!SIGN_IN_NAME_RE.test(name)) return "invalid";
  const have = signIns.find((si) => si.name === name);
  if (have && (have.startUrl !== group[0].startUrl || have.ssoRegion !== group[0].ssoRegion)) return "mismatch";
  if (others.some((o) => o.name === name)) return "taken";
  return null;
}

/** The body of the PUT that moves one profile into the sign-in: the profile's own fields, as the
 *  edit form sends them, plus the sign-in's name. Nothing else is read from the row. */
export function groupBody(p: GroupCandidate, signIn: string): Record<string, unknown> {
  return {
    label: p.label,
    startUrl: p.startUrl,
    ssoRegion: p.ssoRegion,
    accountId: p.accountId,
    roleName: p.roleName,
    region: p.region,
    confirmBeforeUse: p.confirmBeforeUse,
    signIn,
  };
}
