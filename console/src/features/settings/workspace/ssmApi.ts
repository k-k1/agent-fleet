// ssmApi — typed wire shapes and read wrappers for the member's SSM area: profiles, hosts and
// shared sign-ins (control-plane/ssm.go) and the Agent's login states (GET /api/aws-login/profiles,
// workspace/agent/internal/awsx/login_agent.go). The paths stay string literals here:
// control-plane/console_routes_test.go reads them as text to prove each one has a route.
//
// Only reads live in this file. The writes keep their Response-level handling in SsmTab
// (postJSON / deleteRow), ShareSignIn and ssmImportExec, which branch on the status and on the
// refusal's code; what they send is typed by the *Write shapes below.
//
// Every reply is `T | { error }`: api() resolves with the parsed body whatever the status was.
import { api } from "../../../core/api/client.ts";
import type { ApiError } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type { SsmSignIn } from "./ssmSignIns.ts";

/** One profile as GET /api/ssm/profiles lists it (ssmProfileListDTO). */
export type SsmProfile = {
  id: string;
  label: string;
  /** "" for an assume_role profile and for a member of a sign-in that supplies the portal. */
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
  createdAt: string;
  kind: "sso" | "assume_role";
  /** The remaining fields below belong to assume_role only, and are emitted when non-empty. */
  sourceProfileId?: string;
  roleArn?: string;
  externalId?: string;
  sessionName?: string;
  durationSeconds?: number;
  /** Emitted only when true. */
  confirmBeforeUse?: true;
  /** Emitted only when true. */
  modelRoute?: true;
  /** The shared sign-in's name; absent for a sign-in of its own. */
  signIn?: string;
  /** The ~/.aws profile name the workspace knows the row by. */
  name: string;
  /** Emitted only when true: another label maps to the same name. */
  nameCollides?: boolean;
};

/** One host as GET /api/ssm/hosts lists it (ssmHostDTO). */
export type SsmHost = {
  id: string;
  alias: string;
  profileId: string;
  /** "" = the profile's default. */
  region: string;
  instanceId: string;
  documentName: string;
  createdAt: string;
};

// --- What a write sends. Each is the exact key set of its request body. -------------------

/** POST/PUT /api/ssm/profiles[/{id}] for an SSO profile. The CP keeps the stored value of an
 *  absent confirmBeforeUse / modelRoute / signIn, so optional here means "left out". */
export type SsmSsoProfileWrite = {
  label: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
  confirmBeforeUse: boolean;
  modelRoute?: boolean;
  /** A name joins or creates that sign-in; "" leaves the current one. */
  signIn?: string;
};

/** The same endpoint for an assume_role profile. */
export type SsmChainedProfileWrite = {
  kind: "assume_role";
  label: string;
  sourceProfileId: string;
  roleArn: string;
  externalId: string;
  sessionName: string;
  region: string;
  durationSeconds?: number;
  confirmBeforeUse: boolean;
  modelRoute: boolean;
};

export type SsmProfileWrite = SsmSsoProfileWrite | SsmChainedProfileWrite;

/** POST/PUT /api/ssm/hosts[/{id}]. */
export type SsmHostWrite = {
  alias: string;
  profileId: string;
  instanceId: string;
  documentName: string;
  region: string;
};

/** PUT /api/ssm/sign-ins/{id}. */
export type SsmSignInWrite = { name: string; startUrl: string; ssoRegion: string };

/** What a failed write or delete answers (writeAPIErr / ssmProfileInUseResp); the in-use 409
 *  adds the names of what still depends on the profile. */
export type SsmFailBody = { error?: ApiError; hosts?: string[]; profiles?: string[] } | null;

// --- The Agent's login states (GET /api/aws-login/profiles) --------------------------------

export type ProfileLoginState = {
  name: string;
  state: string;
  /** The Settings profile a chained profile follows. */
  source?: string;
  label?: string;
  accountId?: string;
  roleName?: string;
  expiresAt?: string;
  expiresInSec?: number;
  expiring?: boolean;
  signIn?: string;
};

export type SignInLoginState = {
  name: string;
  state: string;
  expiresAt?: string;
  expiresInSec?: number;
  expiring?: boolean;
  profiles: string[];
};

export type LoginStates = { profiles: ProfileLoginState[]; signIns: SignInLoginState[] };

// The callers still test Array.isArray on the lists they read: an Agent older than the sign-ins
// answers without `signIns`, and the states then settle empty rather than throw.
export const listSsmProfiles = (): Promise<Reply<SsmProfile[]>> => api("api/ssm/profiles");
export const listSsmHosts = (): Promise<Reply<SsmHost[]>> => api("api/ssm/hosts");
export const listSsmSignIns = (): Promise<Reply<SsmSignIn[]>> => api("api/ssm/sign-ins");
export const getLoginStates = (): Promise<Reply<LoginStates>> => api("api/aws-login/profiles");
