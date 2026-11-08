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

The model is the one you choose in the launch dialog. Its default is a tier alias (Opus / Sonnet /
Haiku), which claude turns into a Bedrock inference-profile ID for the route's region, and that ID may not exist there:
in `ap-northeast-1`, for example, the default Sonnet ID failed on the first turn with `model_not_found`. Where the
account had no access to the model, claude falls back to older Opus versions, one gateway call (and one usage row) for
each attempt; this is the CLI's behaviour and may change. So use a region where the account has enabled the model. The
launch dialog offers the tier aliases and the IDs you register under **Additional Claude models** in Settings
([agents](../member/09-agents.md)); Settings accepts only IDs that start with `claude-` (for example `claude-opus-4-8`), not AWS
inference-profile IDs such as `us.anthropic.…`, and whether claude maps a registered `claude-…` ID to the right Bedrock
ID on this route has not been verified. AF does not check the model against the account or pin one per route.

## When a call is refused

The gateway answers an authentication or connection refusal itself with HTTP 401 (a refusal
that waiting may clear, 503), always with `x-should-retry: false`, so claude shows it once, with the code in the message,
and does not retry. (Requests the gateway cannot forward keep their own status: `route_operation_not_allowed` 404,
`route_bad_model` 400, `route_request_too_large` 413.)

| Code | Meaning | What to do |
|---|---|---|
| `route_token_invalid` (401) | The route token is missing, unknown, expired, or was revoked (the session stopped, or the profile or selection changed). | Start the session again. |
| `route_caller_forbidden` (401) | The token was presented by a process outside the session it was issued to. The token is not revoked. | Call the model from the session's own terminal, not from another session. |
| `route_connection_forbidden` (401) | The profile was unmarked as a model route or deleted since the session started (at session creation the same refusal is 403). The token is revoked. | Mark the profile again, or start a new session on another profile. |
| `route_session_ended`, `route_selection_changed`, `route_connection_changed` (401) | The session ended, no longer selects this route, or its profile was edited. The token is revoked. | Start a new session. |
| `route_login_required`, `route_confirmation_required` (401) | The profile needs an SSO sign-in or your "Confirm before use" approval. | Sign in or allow it; the next call goes through. |
| `route_connection_unavailable` (503) | The Agent cannot read the AWS profiles in Settings right now. | Try again in a moment. |
| `route_upstream_unavailable`, `route_credentials_unavailable` (503) | Bedrock or the credentials could not be reached right now. | Try again in a moment. |

A Bedrock answer such as AccessDenied or `model_not_found` is forwarded as it is; it is not a gateway code.
