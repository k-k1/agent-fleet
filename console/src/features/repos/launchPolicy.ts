// The credential policy's create refusals (ADR 0138 D11, D12) as the launch screens explain them.
// The Agent's message names route ids and says what to do in English; a member's screens say it
// in their own words and never name what the policy hides (D10a), so only the code is read.

/** The refusals a launch screen explains itself. */
export type PolicyRefusal = "route_forbidden" | "route_required" | "policy_unconfirmed";

const POLICY_REFUSALS: readonly string[] = ["route_forbidden", "route_required", "policy_unconfirmed"];

/** The refusal an API error carries, or null for any other error. */
export function policyRefusalOf(err: unknown): PolicyRefusal | null {
  const code = err && typeof err === "object" ? (err as { code?: unknown }).code : undefined;
  return typeof code === "string" && POLICY_REFUSALS.includes(code) ? (code as PolicyRefusal) : null;
}

/** The catalogue key of a refusal's sentence (launch.policy.<code>). */
export function policyRefusalKey(r: PolicyRefusal): `launch.policy.${PolicyRefusal}` {
  return `launch.policy.${r}`;
}
