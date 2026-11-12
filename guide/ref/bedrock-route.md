---
audience: "anyone running claude or opencode on Amazon Bedrock with one of their AWS profiles"
source_of_truth: "the AWS profiles/SSM tab of the Console's Settings, the claude launch dialog, and the route gateway in the workspace"
---

# Amazon Bedrock route

English | [日本語](bedrock-route.ja.md)

A claude or opencode session can run its models on Amazon Bedrock with one of your Settings > "AWS profiles/SSM" profiles. The
session never receives AWS credentials: a gateway in the workspace signs each model call.

## The mark and the gateway

Mark the profile "Model route" in its form under Settings > "AWS profiles/SSM".

- **What the mark says**: that the profile's role carries only `bedrock:InvokeModel*` (the calls that run a model)
  and nothing broader. It is only your statement: AF cannot read the role's permissions, so mark a profile only when
  you have checked them.
- **What the gateway does**: it runs inside the workspace, signs each model call with the profile's short-lived
  credentials and forwards it, so the session that makes the calls never receives AWS credentials.
- **What it forwards**: only `invoke`, `invoke-with-response-stream`, `converse` and `converse-stream`. It cannot list
  models or call any other AWS operation.
- **An unmarked profile** is refused as a route, with the reason in the answer to the call.
- **The route token** is tied to one session, to the profile as it is now and to the route the session selected. It
  is served only to that session's own processes: the gateway listens on a socket in the workspace and checks which
  process is calling, so another session that copies the token is refused.
- **The token ends** when the session is stopped, deleted, archived or recreated, when the selection changes, and
  when the profile is edited, unmarked or deleted. One that nothing ended runs out ten minutes after its session stops
  running.
- **When a change takes effect**: a create or a start, and any model call that needs new AWS credentials, reads
  Settings again first, so a change you saved before it is enforced at once when that read succeeds. A session that is
  already running and still has credentials the gateway holds can make further calls for up to 30 seconds after the
  change. When the read fails (the workspace cannot reach the Control Plane, or the read takes longer than 15
  seconds), the gateway uses the last list it read successfully, for creates, starts and new credentials as well as for
  calls, until 15 minutes after that read, and then refuses every call.
- **A profile set to "Confirm before use"** is asked about when the session starts, and is checked again on
  every model call: until you allow it, each call fails at once with the reason.
- **To use it**: in the launch dialog of a claude session, choose **Amazon Bedrock** under **Model route** (see
  [the claude routes](agents.md#claude-the-api-key-route)). The option appears only when at least one of your
  profiles is marked. The route is not offered generally yet: for now it appears only on a workspace whose operator
  has turned it on.
- **opencode**: a Terminal (CLI) session can choose it the same way, with one of the **amazon-bedrock/…** models
  ([opencode: the Amazon Bedrock route](agents.md#opencode-the-amazon-bedrock-route)).

## Before the first Bedrock call

What follows was seen in one account, not taken from AWS documentation; check AWS's current Bedrock model-access
documentation for your account.
A model that is already enabled for the account may need none of it.

- Submit Anthropic's use-case form in the Bedrock console.
- When the model has not been invoked in the account before, its first invocation has to come from a principal that
  is allowed `aws-marketplace:Subscribe` (and `aws-marketplace:ViewSubscriptions`). A role with only
  `bedrock:InvokeModel*` is refused with AccessDenied on that first call. Make the first call once with a broader
  principal; after that the narrow route role works.

## Region and model

For a claude session on this route you type the model as Bedrock writes it, in the **Bedrock model ID** field beside the
region: a model ID such as `anthropic.claude-sonnet-4-5-20250929-v1:0`, or a system inference profile ID, which is the same
ID behind `us.`, `eu.`, `apac.`, `jp.`, `au.`, `us-gov.` or `global.` (for example `us.anthropic.claude-opus-4-6-v1`). The
field is required, and the launch dialog's ordinary model picker is hidden while the route is chosen. Pick an ID that the
Bedrock console lists for the route's region and that your account has access to. The session uses that one ID for every
call, its subagents and claude's own small background calls included, and never falls back to another model; a call that
names a different model is refused by the gateway (`route_model_not_selected`) before it reaches AWS. Tier aliases (Opus /
Sonnet / Haiku), `claude-…` IDs and ARNs are refused at launch: with them claude picks Bedrock IDs of its own and, when
the account lacks one, tries older models one after another, one gateway call (and one usage row) each. AF cannot check
the ID against your account (the route's role may only invoke models), so an ID the account or region does not serve
fails on the first turn with Bedrock's own answer, such as AccessDenied or `model_not_found`; start a new session with
another ID. The dialog remembers the last ID per profile in this browser. Over the REST API or the af MCP tools, send the
ID as `model` with the route.

## Budgets and reservations (the deployment's Bedrock route)

This applies when the deployment itself provides the Bedrock route and a tenant administrator sets a monthly budget
per member and a monthly ceiling for the tenant.

- **Reservation**: before a call is sent, the gateway reserves its pre-call upper bound: the model's whole context
  window at its dearest input rate plus the requested output cap at its dearest output rate. The call is admitted only
  when that whole amount fits under both the member's budget and the tenant's ceiling; the reservation is settled
  to the real cost afterwards. Claude Code asks for tens of thousands of output tokens, so its reservation is far
  above that of a short call.
- **Floor**: the smallest budget that admits one call of the cheapest model on the list, that is, the smallest of the
  models' one-token-output reservations. Below it no call of any model can ever be admitted. The tenant view (`GET /api/admin/tenants/{slug}/provided-routes/deployment`) lists
  each model's reservations and the floor (`reservations`, `floor_micro`).
- **A 400 on save**: a member or default budget, or a ceiling while the grant is on, above zero and below the floor is
  refused, because no call could ever be admitted. Set at least the floor, or 0 for no use. 0 is accepted for a budget
  and for a ceiling while the grant is off; a ceiling of 0 with the grant on is refused for a different reason (a
  granted tenant needs a ceiling above zero). While the grant is off the ceiling is inert and is not checked against the floor.
- **A warning on save**: an amount at or above the floor but below a model's full-output reservation is saved, and the
  answer carries a warning. Calls fit only while the output cap they ask for is small.
- **A 503 at call time** (`route_budget_exhausted`): the call's reservation does not fit in what is left of the
  budget or ceiling this month. When nothing has been spent, the message says the budget is smaller than one call's
  reservation. Ask the tenant administrator to raise it. A refused call writes no usage row.

## When a call is refused

The gateway answers a refusal itself, always with `x-should-retry: false`. A refusal about the route token or the AWS login goes out as HTTP 401, one about what you
may use (a profile that is not marked, a grant that is off, a model that is not allowed) as 412, one that waiting may
clear as 503. Claude Code prints "AWS authentication failed · refresh your AWS credentials …" before the message of a
401 only; the 401, 412 and 503 refusals are shown once, not retried. (Requests the gateway cannot forward keep their own status, and claude may retry them (a 400 goes out eight times): `route_operation_not_allowed` 404,
`route_model_not_selected` 404, `route_bad_model` 400, `route_request_too_large` 413.)

| Code | Meaning | What to do |
|---|---|---|
| `route_token_invalid` (401) | The route token is missing, unknown, expired, or was revoked (the session stopped, or the profile or selection changed). | Start the session again. |
| `route_caller_forbidden` (401) | The token was presented by a process outside the session it was issued to. The token is not revoked. | Call the model from the session's own terminal, not from another session. |
| `route_connection_forbidden` (412) | The profile was unmarked as a model route or deleted since the session started (at session creation the same refusal is 403). The token is revoked. | Mark the profile again, or start a new session on another profile. |
| `route_session_ended`, `route_selection_changed` (401), `route_connection_changed` (401, or 412 when the profile is found edited at a call) | The session ended, no longer selects this route, or its profile was edited. The token is revoked. | Start a new session. |
| `route_login_required`, `route_confirmation_required` (401) | The profile needs an SSO sign-in or your "Confirm before use" approval. | Sign in or allow it; the next call goes through. |
| `route_connection_unavailable` (503) | The Agent cannot read the AWS profiles in Settings right now. | Try again in a moment. |
| `route_upstream_unavailable`, `route_credentials_unavailable` (503) | Bedrock or the credentials could not be reached right now. | Try again in a moment. |
| `route_model_mismatch` (400, at launch) | A claude session on this route was started with a model that is not a Bedrock model ID: empty, a tier alias such as `opus`, a `claude-…` ID or an ARN (from the launch dialog, the REST API, the af MCP tools or a schedule). Nothing was started. | Send a Bedrock model ID such as `us.anthropic.…` as the model. |
| `route_model_not_selected` (404) | The call named a model other than the Bedrock model ID the session was started with. Nothing was sent to AWS; the token stays valid. | Start a new session with the other ID. |

A Bedrock answer such as AccessDenied or `model_not_found` is forwarded as it is; it is not a gateway code.
