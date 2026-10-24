---
audience: "everyone, but written for whoever is looking for a knob and cannot find it"
source_of_truth: "the Console for personal and tenant settings; `deploy/compose/.env.example` for deployment variables"
---

# Settings — where things are configured

English | [日本語](settings.ja.md)

Three layers set behaviour, and confusing them is the usual reason a change appears to
have no effect. From narrowest to widest:

| Layer | Set by | Where | Takes effect |
|---|---|---|---|
| Personal | you | account menu → **Settings** | immediately, or from your next session |
| Tenant | a tenant administrator | account menu → **Tenant settings** | on the members of that tenant |
| Deployment | whoever operates the deployment | environment, before start | on restart |

**A narrower layer cannot widen a wider one.** A personal setting cannot exceed a
tenant limit, and a tenant setting cannot exceed what the deployment allows. When a
value you set is not the value in force, look one layer out.

## Personal settings

| Tab | Configures |
|---|---|
| Display | language, theme, density |
| Keyboard | shortcuts and rebinding |
| Agents | connecting each agent, its default model, models to exclude, per-agent behaviour; llama.cpp on / off and your own llama-server connection; Muse Code install and sign-in; the Session group (messages between sessions, starting sessions from sessions and children per session, past-session search, image generation) |
| Agent instructions | text added to every agent you start in this workspace |
| Agent memory | Agent Fleet's own shared memory, in four views: Memories (browse, search, create, edit, pin, forget, unforget, purge), Changes (the change list, with revert and forget, and **Proposed**: memories sessions suggested, to approve or reject), Import & export (a hub of cards: import from Claude Code, muse or a memory bundle, write back to Claude Code, project move) and Settings (the sessions' switch with the sync status, outside access, **Have finished sessions suggest memories** and **Also suggest automatically**); see [member/18](../member/18-agent-memory.md) |
| Memory snapshots | version management of each agent kind's own memory, in three views: History (with Restore this point; the Agent Fleet memory writes are hidden unless you ask), Targets (the roots, codex memories, automatic snapshots) and Export & import |
| Assistant | the assistant chat's agent and model |
| AI assistance | default agent/models for titles, branch names, reply/edit suggestions, plan updates and translation; per-feature on/off plus per-feature agent/model override |
| Agent usage | your token spend, by feature, agent and model |
| Cloud cost | your share of the deployment's cloud spend |
| Running time | when your workspace was running (a 24-hour x date heatmap) |
| Git hosting | GitHub / Bitbucket connections |
| Internal repos | repositories hosted by the deployment itself |
| AWS profiles/SSM | AWS profiles and remote login targets |
| Google Cloud | Google Cloud profiles that `af-gcloud-exec` runs commands with |
| Issue tracker | Jira and the other work-item sources |
| Chat | Discord / Slack bridge |
| MCP servers (used by sessions) | integration servers available to your agents |
| MCP tokens & apps | tokens for driving your sessions from outside and, with the Agent memory setting, for using your Agent Fleet memory from an outside agent |
| Notifications | what you are told about, and how |
| Limit board | who in this tenant may see where you stand on usage limits, and how much (hidden by default); what teammates publish to you |
| Read aloud | speech engine and voice |
| Machine | the instance your workspace runs on (kind, architecture, vCPU, memory, disk) |
| Toolchains | JDK and other per-workspace toolchains |
| Databases | this workspace's PostgreSQL / MySQL for database-backed tests (install, start, stop, create, reset) |
| Preview subdomains | the URL that opens the app you are building (only where they are issued) |
| Ops & monitoring | monitoring integrations |
| Export / import | take your settings to another deployment |
| Account | sign-in methods linked to your account |
| Danger zone | destructive actions on your own workspace |

## Tenant settings

| Tab | Configures |
|---|---|
| Members | the roster; per-member resources, sessions and operations |
| Engine access | per role (chat `llm`, image generation `image`) of the deployment's self-hosted engines: every member, or only the members you tick, within what the deployment administrator allows the tenant |
| Sessions | everything running in the tenant right now |
| Limits & idle | the limits in force (read-only; a deployment administrator sets them) |
| Limit board | whether the tenant's members can publish their usage-limit state at all (on by default; each member is still hidden until they opt in) |
| Sign-in methods | your own IdP or GitHub organisation as a way in (needs approval) |
| Login rules | join mode and domains in force (read-only) |
| Allowed networks | where members may connect from; the same screen carries the **MCP OAuth** panel, which decides whether outside apps may connect to the tenant through OAuth ([admin 04](../admin/04-mcp-outside.md#mcp-oauth)) |
| Integration OAuth apps | your tenant's own OAuth apps for GitHub / Bitbucket |
| MCP distribution | integration servers handed to every member |
| Branch naming rules | the tenant's default for naming new branches and choosing their base, per repository pattern (a JSON list); members' Workspaces pick it up within five minutes. Advice only: a name that does not follow it gets a warning, never a refusal |
| External memory access | whether members may use their Agent Fleet memory from an agent outside Agent Fleet through `/mcp`. Allow (default) lets members opt in; Block withholds the memory tools from the whole tenant ([admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet)) |
| Member defaults | the agent the first-run card names in its "Connect an agent" step (Claude, Codex, opencode, Cursor, Copilot, Kiro, Muse, or none). Members read it whenever the Console loads. It names, never restricts: connecting any other agent completes the step ([admin 06](../admin/06-team-defaults.md#member-defaults)) |
| Audit | who changed what, when |
| Running time | per-member workspace uptime, exportable, plus an hour-by-hour heatmap of the whole tenant |
| Cloud cost | the tenant's cloud spend, per member, only where the deployment runs on AWS ([admin 03](../admin/03-audit-usage.md#cloud-cost-cloud-cost)) |
| Inference engine models | opens the model catalogue to add models to the self-hosted engines (only where the deployment administrator granted the tenant model ingest; enabling a model stays with the deployment administrator) |

## Admin modal

The shield-icon **Admin** item, which only a deployment administrator sees ([roles.md](roles.md)). The
rail has three groups: **Tenants**, **Deployment** and **Across tenants**. Some items exist only where
the deployment supports them: **Slots** on the `ecs-ec2` target, **Inference engines** and **API tokens**
wherever the Control Plane serves the engine panel, **Cloud cost** on AWS.

| Section | Configures | Details |
|---|---|---|
| Tenants | creating tenants; each tenant's limits and idle auto-stop, its use of the self-hosted engines and its model-ingest grant, its members | [operate/02](../operate/02-install.md#7-the-first-tenant-and-members) |
| Sign-in method register | approving the sign-in methods tenants define for themselves | [operate/05](../operate/05-signin.md#7-a-sign-in-method-a-tenant-defines-itself) |
| Egress | outbound traffic from workspaces: observe (log-only) or enforce, the allowlist, members' proposals | [operate/04](../operate/04-secure.md#operating-egress-control) |
| MCP sign-in hosts | which client hosts the MCP OAuth server trusts (deployment-wide) | [operate/04](../operate/04-secure.md#trusted-client-hosts-deployment-wide-superadmin-only) |
| Read aloud | the speech engine's mode, and the shared pronunciation dictionary | [operate/07](../operate/07-engines.md#read-aloud-the-speech-engine-and-the-shared-dictionary) |
| Appearance | the deployment's colour and label, to tell one deployment from the next | [operate/03](../operate/03-run.md#telling-two-deployments-apart-colour-and-label) |
| Dev sign-in | the development OIDC IdP switch, for apps on preview subdomains | [operate/04](../operate/04-secure.md#development-sign-in-dev-oidc-idp-superadmin-only) |
| Inference engines | the self-hosted engines: mode, GPU class, model catalogue, state and history | [operate/07](../operate/07-engines.md) |
| API tokens | the Hugging Face and Civitai tokens, and the Civitai Red switch, for taking models in | [operate/07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| Slots | the `ecs-ec2` slot pool: replacing slots, force-stopping a wedged one | [operate/11](../operate/11-slot-pool.md) |
| Sessions, Running time, Cloud cost, Audit, MCP | the tenant views, across every tenant | [admin/](../admin/README.md) |

## Deployment variables

Set before the Control Plane starts. The annotated list is
`deploy/compose/.env.example`, which covers most variables but not all; these are the ones that decide the shape of
everything else:

| Variable | Decides |
|---|---|
| `AF_RUNTIME` | the deployment target; see [deploy-targets.md](deploy-targets.md). Rejected at boot if unknown |
| `AUTH` | how people sign in: `dev` (single user), `oauth` (the Control Plane's own), `proxy` (an upstream gateway) |
| `DATA_DIR` | where all persistent state lives (the thing to back up) |
| `AF_MASTER_KEY` | the root of at-rest encryption. Lose it and the stored credentials are unrecoverable |
| `AF_KEY_CUSTODIAN` | who holds the at-rest keys: `local` (the default, derived from `AF_MASTER_KEY`) or `kms` (AWS KMS, with `AF_KMS_KEY_ID`; [operate/12](../operate/12-keys.md#keys-at-rest-on-aws-kms)). Rejected at boot if unknown or incomplete |
| `AF_WORKSPACE_DEK` | `derived` (the default) or `random`: a KMS-sealed key of its own for each member's home, so a credential store that has moved to it no longer opens with a key derived from `AF_MASTER_KEY` and the Control Plane cannot unwrap it once the KMS key is disabled (copies already handed to the workspace and its runtime still open it). `random` needs `AF_KEY_CUSTODIAN=kms`; turning it off does not undo it ([operate/12](../operate/12-keys.md#a-key-of-its-own-for-each-home)) |
| `SUPER_ADMIN_EMAILS` | who is a deployment administrator |
| `PUBLIC_BASE_URL` | the address people reach, and what OAuth callbacks are built from |
| `WS_MEMORY` | the default memory ceiling for a workspace |
| `AF_PREVIEW_DOMAIN` | the parent domain of the preview subdomains (`<workspace>-<port>.<domain>`), which needs a wildcard DNS name and certificate for it. Unset means no preview subdomains, only the Console's own preview path. Set `AF_COOKIE_SECRET` and `PUBLIC_BASE_URL` with it: the Control Plane warns at start when either is missing, because preview sign-in then cannot be trusted or has no Console to return to |
| `AF_DEV_IDP` | `0` locks the development sign-in (an OIDC provider on the Console's origin that a member's app on a preview subdomain can sign in with in place of Google) off, whatever the deployment administrator's switch says. Unset leaves it to that switch, which is on by default. It is only served where `AF_PREVIEW_DOMAIN` and `PUBLIC_BASE_URL` are set |
| `AF_MCP_ENABLED` | `true` serves the `/mcp` endpoint (Personal Access Tokens); anything else, including `1`, leaves it off. The tenant-level memory switch ([admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet)) is a different, narrower switch |
| `AF_MCP_OAUTH` | `1` also makes the Control Plane an OAuth authorization server for `/mcp`, so hosted clients connect by signing the member in. Needs `AF_MCP_ENABLED=true` and an `https` `PUBLIC_BASE_URL`; with any of the three missing the OAuth paths answer 404. Each tenant can still switch it off for itself ([admin 04](../admin/04-mcp-outside.md#mcp-oauth)); [operate/04](../operate/04-secure.md#sign-in-for-remote-mcp-clients-oauth) has the rest |
| `AF_MCP_OAUTH_TRUSTED_HOSTS` | the client hosts MCP OAuth trusts, comma-separated; the default is `claude.ai`, `claude.com` and `chatgpt.com`, and `none` leaves loopback clients only. A list saved in the Admin modal (**MCP sign-in hosts**) wins over this variable ([operate/04](../operate/04-secure.md#trusted-client-hosts-deployment-wide-superadmin-only)) |
| `AF_EFS_BACKUP` | not set by hand: on `ecs` / `ecs-ec2`, `update.sh` and `standup.sh` fill it from the ingress stack's `EfsBackup` parameter (`present`, `off` or `missing`), and `missing` raises the Admin screen's "no daily EFS backup" banner ([operate/03](../operate/03-run.md#upgrade)) |
| `AF_SESSION_IDLE_TIMEOUT`, `AF_WS_IDLE_TIMEOUT`, `AF_BACKGROUND_HOLD_MAX`, `AF_WORKSPACE_START_DEADLINE` | when idle sessions and Workspaces are stopped; the defaults are in [limits](limits.md#set-by-the-deployment) and the behaviour in [operate/03](../operate/03-run.md#idle-stop-and-force-stop) |

### On AWS ECS

The ECS stacks take no free-form environment: a Control Plane variable reaches the task only
through a `30-ingress.yaml` parameter. Every parameter below defaults to empty (or `off`), and an
empty one adds no environment entry, so the Control Plane keeps its own default.

| Variables | Stack parameters |
|---|---|
| `AF_SESSION_IDLE_TIMEOUT`, `AF_INTERACTION_IDLE_TIMEOUT`, `AF_WS_IDLE_TIMEOUT`, `AF_PRESENCE_IDLE_TIMEOUT`, `AF_BACKGROUND_HOLD_MAX`, `AF_WORKSPACE_START_DEADLINE`, `AF_USAGE_SAMPLE_INTERVAL` | `SessionIdleTimeout`, `InteractionIdleTimeout`, `WsIdleTimeout`, `PresenceIdleTimeout`, `BackgroundHoldMax`, `WorkspaceStartDeadline`, `UsageSampleInterval` (Go durations; `0` turns the timer off) |
| `AF_GITHUB_LOGIN_CLIENT_ID`, `AF_GITHUB_LABEL_JA`, `AF_GITHUB_LABEL_EN`, `AF_GITHUB_MEMBERSHIP_TTL`, `AF_GITHUB_MEMBERSHIP_GRACE` | `GithubLoginClientId`, `GithubLabelJa`, `GithubLabelEn`, `GithubMembershipTtl`, `GithubMembershipGrace` |
| `AF_LLM_URL`, `AF_COMFY_URL`, `AF_REMOTE_ENGINE_URL`, `AF_REMOTE_ENGINE_KEYS` | `LlmUrl`, `ComfyUrl`, `RemoteEngineUrl`, `RemoteEngineKeys` |
| `AF_GITHUB_LOGIN_CLIENT_SECRET`, `AF_COMFY_API_KEY`, `AF_REMOTE_ENGINE_TOKEN` | the switches `GithubLoginSecret`, `ComfyApiKey`, `RemoteEngineToken` (`on`): the value is read from the SSM SecureString `<SsmPrefix>/github-login-client-secret`, `/comfy-api-key`, `/remote-engine-token`, which must exist first |

`WS_ENV` has no parameter on ECS on purpose: it carries free-form `KEY=VAL` pairs, secrets
included, in plain environment. The parameters are set with a targeted `aws cloudformation deploy` of `30-ingress.yaml`
(`update.sh` overrides only `ImageTag` and `EfsBackup`); see
[`deploy/aws/ecs/cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines).
A LAN engine is reachable from ECS only when the VPC has a route to it (VPN, peering).
