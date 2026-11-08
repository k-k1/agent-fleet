// connectApi — typed wrappers for the endpoints behind the git-hosting (GitTab) and
// operations (OpsTab) connection cards. The paths stay string literals here:
// control-plane/console_routes_test.go reads them as text to prove each one has a route.
//
// Replies follow api.ts: `T | { error }`, told apart by `isOk`. Request types list exactly the
// members the cards send; a credential only ever travels in the member named below, and no type
// here carries a response field that could hold one (the Agent echoes status, never a secret).
import { api, apiJSON, raw, type ApiError } from "../../../core/api/client.ts";
import type { ConnectionsStatus, ProviderConn } from "../../../types/session.ts";
import type { Reply } from "../api.ts";

/** The error member of a failed reply; undefined for a real answer (or no answer at all). */
export const replyError = (r: Reply<unknown> | null | undefined): ApiError | undefined =>
  (r as { error?: ApiError } | null | undefined)?.error || undefined;

// --- GET /api/connections: the slices of an entry the cards read -------------------------

/** A git-hosting entry (workspace/agent/connections.go gitConnStatus / bitbucketStatus).
 *  `username` / `email` / `commit*` appear only once an account is known. */
export type GitHostConn = ProviderConn & {
  username?: string;
  email?: string;
  commitName?: string;
  commitEmail?: string;
  /** GitHub only: the stored token can no longer be renewed. */
  reconnect_needed?: boolean;
};

/** PagerDuty: `host` is set only for the EU override. */
export type PagerDutyConn = ProviderConn & { host?: string };

/** CloudWatch (awsProfileStatus): the profile name, plus `region` / `sso` when set. */
export type CloudWatchConn = ProviderConn & { profile?: string; region?: string; sso?: boolean };

/** Agent Toolkit for AWS: the CloudWatch fields plus the endpoint region and the write opt-in. */
export type AwsMcpConn = CloudWatchConn & { endpoint?: string; write?: boolean };

export const getConnections = (): Promise<Reply<ConnectionsStatus>> => api("api/connections");

// --- GET /api/git-oauth (CP-native: which OAuth buttons the tenant can offer) -------------

export type GitOAuthAvailability = {
  configured?: boolean;
  /** GitHub only: set when the tenant's app is a GitHub App (issue #1667). */
  app_type?: "github_app";
  install_url?: string;
};

export const getGitOAuthAvailability = (): Promise<Reply<Record<string, GitOAuthAvailability>>> =>
  api("api/git-oauth");

// --- GitHub device flow (control-plane/oauth_github_device.go) ---------------------------

export type GithubOAuthStart = {
  flow_id: string;
  user_code: string;
  verification_uri: string;
  /** Seconds between polls. */
  interval: number;
  /** Seconds until the device code lapses. */
  expires_in: number;
};

/** One poll answer: `pending` (optionally with a slowed `interval`) or `connected` with what
 *  still stands between the member and a working clone. */
export type GithubOAuthPoll = {
  pending?: boolean;
  interval?: number;
  connected?: boolean;
  /** The token carries no renewal, so it stops working after about eight hours. */
  token_expires?: boolean;
  install_url?: string;
  /** The GitHub App is installed on no repository. */
  not_installed?: boolean;
};

export const startGithubOAuth = (): Promise<Reply<GithubOAuthStart>> =>
  api("api/connections/git/github/oauth/start", { method: "POST" });
export const pollGithubOAuth = (flowId: string): Promise<Reply<GithubOAuthPoll>> =>
  apiJSON("api/connections/git/github/oauth/poll", "POST", { flow_id: flowId });

// --- Bitbucket OAuth start (control-plane/oauth_bitbucket.go) ----------------------------

export type BitbucketOAuthStart = { authorize_url: string };

export const startBitbucketOAuth = (): Promise<Reply<BitbucketOAuthStart>> =>
  api("api/connections/git/bitbucket/oauth/start");

// --- Pasted-token connections (workspace/agent/connections.go handlePutGitConn) ------------

/** The answer of a token save: `warn` is "no_write" (Bitbucket) when the token cannot push,
 *  `renewable` only for a GitHub App pair. */
export type GitConnectReply = {
  connected: boolean;
  host: string;
  username: string;
  renewable?: boolean;
  warn?: string;
};

export const connectGithubToken = (token: string): Promise<Reply<GitConnectReply>> =>
  apiJSON("api/connections/git/github.com", "PUT", { token });
export const connectBitbucketToken = (username: string, token: string): Promise<Reply<GitConnectReply>> =>
  apiJSON("api/connections/git/bitbucket.org", "PUT", { username, token });
export const disconnectGithub = (): Promise<Response> => raw("api/connections/git/github.com", { method: "DELETE" });
export const disconnectBitbucket = (): Promise<Response> =>
  raw("api/connections/git/bitbucket.org", { method: "DELETE" });

// --- Commit identity ----------------------------------------------------------------------

/** The answer of the global identity endpoints (workspace/agent/internal/gitx/git_identity.go). */
export type GitIdentity = { name: string; email: string };
/** The answer of the per-provider save: the identity now in effect for that host. */
export type GitProviderIdentity = GitIdentity & { host: string };

export const getGlobalGitIdentity = (): Promise<Reply<GitIdentity>> => api("api/git/identity");
export const putGlobalGitIdentity = (id: GitIdentity): Promise<Reply<GitIdentity>> =>
  apiJSON("api/git/identity", "PUT", id);
export const putGitProviderIdentity = (host: string, id: GitIdentity): Promise<Reply<GitProviderIdentity>> =>
  apiJSON(`api/connections/git/${encodeURIComponent(host)}/identity`, "PUT", id);

// --- Subversion (workspace/agent/internal/svnx) --------------------------------------------

/** One saved server in GET /connections `svn`: the URL prefix a credential is matched by, the
 *  account, and whether the certificate is trusted. Never the password. */
export type SvnServer = {
  urlPrefix: string;
  username?: string;
  /** "1" when set — the Agent folds these into GET /connections as plain strings. */
  trustCert?: string;
};

/** PUT body. `password` is the only secret; the optional `verifyUrl` is never sent from here. */
export type SvnPut = { urlPrefix: string; username: string; password: string; trustCert: boolean };
export type SvnPutReply = { urlPrefix: string; username: string; trustCert: boolean };

export const putSvnServer = (body: SvnPut): Promise<Reply<SvnPutReply>> =>
  apiJSON("api/connections/svn", "PUT", body);
export const deleteSvnServer = (prefix: string): Promise<Response> =>
  raw(`api/connections/svn?prefix=${encodeURIComponent(prefix)}`, { method: "DELETE" });

// --- Operations connections (workspace/agent/connections.go) -------------------------------

/** The slice of an SSM profile the AWS picker needs (GET /api/ssm/profiles). The SSO members
 *  are "" for a profile without them. */
export type SsmProfilePick = {
  id: string;
  label: string;
  region: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
};

/** The request half CloudWatch and the AWS MCP card share: a manual entry names a profile only;
 *  a picked SSM profile also carries its non-secret SSO meta. */
export type AwsProfileBody = {
  profile: string;
  region: string;
  startUrl?: string;
  ssoRegion?: string;
  accountId?: string;
  roleName?: string;
};

export type PagerDutyPut = { apiKey: string; host: string };
export type GrafanaPut = { url: string; token: string };
export type AwsMcpPut = AwsProfileBody & { endpoint: string; write: boolean };

/** What every ops PUT answers: the new status (never the key or token). */
export type OpsConnReply = { connected: boolean; host?: string; url?: string; profile?: string };

export const listSsmProfilePicks = (): Promise<Reply<SsmProfilePick[]>> => api("api/ssm/profiles");

export const putPagerDuty = (body: PagerDutyPut): Promise<Reply<OpsConnReply>> =>
  apiJSON("api/connections/pagerduty", "PUT", body);
export const putGrafana = (body: GrafanaPut): Promise<Reply<OpsConnReply>> =>
  apiJSON("api/connections/grafana", "PUT", body);
export const putCloudWatch = (body: AwsProfileBody): Promise<Reply<OpsConnReply>> =>
  apiJSON("api/connections/cloudwatch", "PUT", body);
export const putAwsMcp = (body: AwsMcpPut): Promise<Reply<OpsConnReply>> =>
  apiJSON("api/connections/aws", "PUT", body);

export const deletePagerDuty = (): Promise<Response> => raw("api/connections/pagerduty", { method: "DELETE" });
export const deleteGrafana = (): Promise<Response> => raw("api/connections/grafana", { method: "DELETE" });
export const deleteCloudWatch = (): Promise<Response> => raw("api/connections/cloudwatch", { method: "DELETE" });
export const deleteAwsMcp = (): Promise<Response> => raw("api/connections/aws", { method: "DELETE" });
