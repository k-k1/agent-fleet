// The calls of the sign-in flow (control-plane/oauth_mcp_client.go). The pure parts live in
// mcpOAuth.ts so they can be unit-tested without the API client.
import { apiJSON, api } from "../../../core/api/client.ts";
import { asRegistration, normalizeStatus, safeAuthorizeUrl } from "./mcpOAuth.ts";
import type { OAuthRegistration, OAuthStatus } from "./mcpOAuth.ts";

const idPath = (id: string) => `api/mcp-servers/${encodeURIComponent(id)}/oauth`;

export async function fetchOAuthStatus(id: string): Promise<OAuthStatus | { error: unknown }> {
  const res = await api(idPath(id));
  if (res && res.error) return { error: res.error };
  return normalizeStatus(res, id);
}

export interface StartOk {
  authorizeUrl: string;
  issuer: string;
  registration?: OAuthRegistration;
}
// A refusal: `code` is the wire code ("client_id_needed" asks for a client id for `issuer`).
export interface StartRefused {
  error: { code?: string; message?: string; issuer?: string; registration?: string };
}

export async function startSignIn(
  id: string,
  opts: { registration?: "dcr" | "cimd" | "preregistered"; scope?: string } = {},
): Promise<StartOk | StartRefused> {
  const res = await apiJSON(idPath(id) + "/start", "POST", opts);
  if (res && res.error) return { error: typeof res.error === "object" ? res.error : { message: String(res.error) } };
  const url = safeAuthorizeUrl(res?.authorize_url);
  if (!url) return { error: { code: "bad_authorize_url" } };
  return { authorizeUrl: url, issuer: String(res.issuer ?? ""), registration: asRegistration(res.registration) };
}

export async function disconnectOAuth(id: string): Promise<{ error?: unknown }> {
  const res = await apiJSON(idPath(id), "DELETE");
  return res && res.error ? { error: res.error } : {};
}

export async function saveClientId(
  id: string,
  issuer: string,
  clientId: string,
  clientSecret: string,
): Promise<{ error?: unknown }> {
  const res = await apiJSON(idPath(id) + "/client", "PUT", {
    issuer,
    clientId: clientId.trim(),
    ...(clientSecret ? { clientSecret } : {}),
  });
  return res && res.error ? { error: res.error } : {};
}

