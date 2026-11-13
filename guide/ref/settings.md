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

Open it with **⚙ Settings** in the account menu of the top bar. The left rail is split into four groups, and the table below follows the same order.

| Group | Tabs |
|---|---|
| Personal | Display, Account, Keyboard, Read aloud, Notifications, Limit board, Assistant, AI assistance, Agent instructions, Skills, Agent memory, Memory snapshots |
| Connections | Agents, Git hosting, Ops & monitoring, Issue tracker, Chat, MCP servers (used by sessions), AWS profiles/SSM, Google Cloud |
| Outside access | MCP tokens & apps |
| Workspace | Agent usage, Cloud cost, Running time, Machine, Toolchains, Databases, Preview subdomains, Internal repos, Export / import, Danger zone |

- It remembers the tab you opened last and reopens there.
- On a phone it opens as a list, then the tab. Back returns to the list; back again closes it.
- Some tabs need the workspace running, because the setting itself lives inside the workspace. While it is stopped the tab says so and offers a "Start workspace" button.
- Which settings are kept in this browser only and which follow you to another PC is in [Change the display and your account](../user/settings/display.md#where-it-is-saved).

| Tab | Configures | Page |
|---|---|---|
| Display | language, region format, time zone, theme, density | [Change the display and your account](../user/settings/display.md) |
| Account | sign-in methods linked to your account | [Change the display and your account](../user/settings/display.md#add-a-sign-in-method) |
| Keyboard | shortcuts and rebinding | [Use the keyboard and the command palette](../user/start/shortcuts.md#change-the-keys) |
| Read aloud | speech engine and voice | [Use read-aloud](../user/settings/read-aloud.md) |
| Notifications | what you are told about, and how | [Get notified](../user/settings/notifications.md) |
| Limit board | who in this tenant may see where you stand on usage limits, and how much (hidden by default); what teammates publish to you | [Show each other where you stand on usage limits](../user/team/usage-limits.md) |
| Assistant | the assistant chat's agent and model | [Ask an assistant](../user/chat/assistant.md#choose-the-reply-language-and-the-models), [Hand work to the Fleet Operator](../user/fleet/operator.md#adjust-the-automatic-replies) |
| AI assistance | default agent/models for titles, branch names, reply/edit suggestions, plan updates and translation; per-feature on/off plus per-feature agent/model override | [Use translation and purpose-built assistants](../user/chat/purpose-assistants.md#set-up-ai-assistance) |
| Agent instructions | text added to every agent you start in this workspace | [Write down how you work, once](../user/agents/instructions.md) |
| Skills | each agent's personal skills (Claude, Codex, OpenCode, Muse): the folders read, which copy wins, and each file's content; read-only | [Bring your Claude Code setup](../user/agents/bring-claude-setup.md#check-your-skills) |
| Agent memory | Agent Fleet's own shared memory, in four views: Memories (browse, search, create, edit, pin, forget, unforget, purge), Changes (the change list, with revert and forget), Import & export (a hub of cards: import from Claude Code, muse or a memory bundle, write back to Claude Code, project move) and Settings (the sessions' switch with the sync status, outside access) | [Read and correct agent memory](../user/memory/overview.md) |
| Memory snapshots | version management of each agent kind's own memory, in three views: History (with Restore this point; the Agent Fleet memory writes are hidden unless you ask), Targets (the roots, codex memories, automatic snapshots) and Export & import | [Restore memory from a snapshot](../user/memory/snapshots.md) |
| Agents | connecting each agent, its default model, models to exclude, per-agent behaviour; llama.cpp on / off and your own llama-server connection; Muse Code install and sign-in; the Sessions group (when stopped sessions are archived, messages between sessions, starting sessions from sessions and children per session, past-session search with the `search_sessions` tool (the command palette's Conversations mode works either way), image generation and the image provider order) | [Choose an agent](../user/agents/choose.md), [Stop and resume a session](../user/sessions/stop-resume.md#leave-a-session-stopped), [Let sessions message each other](../user/fleet/messages.md), [Start child sessions from a session](../user/fleet/child-sessions.md), [Have a session make images](../user/images/from-session.md) |
| Git hosting | GitHub / Bitbucket connections | [Connect a Git service](../user/code/connect-git.md) |
| Ops & monitoring | monitoring integrations | [Connect monitoring tools](../user/integrations/ops-monitoring.md) |
| Issue tracker | Jira and the other work-item sources | [Connect an issue tracker](../user/integrations/issue-tracker.md) |
| Chat | Discord / Slack bridge | [Connect Discord and Slack](../user/integrations/chat-bridge.md) |
| MCP servers (used by sessions) | integration servers available to your agents | [Add an MCP server to your sessions](../user/integrations/mcp-servers.md) |
| AWS profiles/SSM | AWS profiles and remote login targets | [Use AWS SSO](../user/integrations/aws-sso.md), [Log in to EC2 with SSM](../user/integrations/aws-ssm.md) |
| Google Cloud | Google Cloud profiles that `af-gcloud-exec` runs commands with | [Use Google Cloud](../user/integrations/gcloud.md) |
| MCP tokens & apps | tokens for driving your sessions from outside and, with the Agent memory setting, for using your Agent Fleet memory from an outside agent | [Drive your workspace from an external Claude](../user/outside/external-claude.md), [Connect a remote MCP client by signing in](../user/outside/oauth-client.md) |
| Agent usage | your token spend, by feature, agent and model | [Check prices and usage](../user/agents/usage.md#see-what-your-tokens-went-on) |
| Cloud cost | your share of the deployment's cloud spend | [See running time and cloud cost](../user/workspace/cost.md#see-the-cloud-cost) |
| Running time | when your workspace was running (a 24-hour x date heatmap) | [See running time and cloud cost](../user/workspace/cost.md#see-the-running-time) |
| Machine | the instance your workspace runs on (kind, architecture, vCPU, memory, disk), its usage, and memory pressure (measured only) | [See your machine and its usage](../user/workspace/machine.md) |
| Toolchains | JDK and other per-workspace toolchains; agent CLI updates; start at sign-in (only where your tenant has it on); automatic image refresh | [Choose your toolchains](../user/workspace/toolchains.md), [Start your workspace when you sign in](../user/workspace/toolchains.md#start-your-workspace-when-you-sign-in), [Restart onto a new image automatically](../user/workspace/toolchains.md#restart-onto-a-new-image-automatically) |
| Databases | this workspace's PostgreSQL / MySQL for database-backed tests (install, start, stop, create, reset) | [Run database-backed tests](../user/workspace/databases.md#manage-them-from-the-console) |
| Preview subdomains | the URL that opens the app you are building (only where they are issued) | [Use preview subdomains](../user/preview/subdomains.md), [Try Google sign-in on a preview](../user/preview/dev-signin.md) |
| Internal repos | repositories hosted by the deployment itself | [Use the built-in Git](../user/code/internal-git.md) |
| Export / import | take your settings to another deployment | [Export and import your settings](../user/workspace/export-import.md) |
| Danger zone | destructive actions on your own workspace | [Recreate the workspace](../user/workspace/recreate.md) |

### When a change takes effect

Getting this wrong is what makes a setting look like it "didn't work".

| Timing | What |
|---|---|
| Immediately | Display, keys, speech, notifications; adding and removing connections; the stopped-session archive period (on the next session-list refresh); start at sign-in and automatic image refresh on Toolchains |
| From the next session you start | Agent behaviour settings, agent instructions, session-to-session messaging, starting sessions from sessions, past-session search (turning it off also refuses running sessions' searches at once), Agent Fleet memory (likewise), image generation, MCP servers |
| From the next chat message | Assistant settings; ops & monitoring connections (when used from an assistant) |
| After stopping and starting the workspace | Toolchains (time zone, language versions); Machine (a size or class your admin changed) |

### What you want to do → which tab

| What you want | Tab |
|---|---|
| Text is too small / change the colours | Display |
| Sign in with another method too | Account |
| Send with Enter | Keyboard (send key) |
| Switch panes by tab | Display (main area layout) |
| Rebind a shortcut / pass keys to the terminal | Keyboard |
| Have replies read out loud | Read aloud |
| Silence Slack without disconnecting | Notifications (service notifications) |
| Make the assistant answer in English | Assistant (output language) |
| Let the operator run ahead, or stop it | Assistant (autopilot, auto-reply) |
| Stop AI from writing titles or reply suggestions | AI assistance (per-feature on/off) |
| Run only the assistance on a cheap model | AI assistance (priority, models) |
| I keep typing the same preamble | Agent instructions |
| Sign in to Claude / Codex | Agents |
| Stop a model that bills extra from being picked | Agents (models to exclude) |
| Keep stopped sessions in the list longer, or out of it sooner | Agents (archive stopped sessions after) |
| Let sessions talk to each other | Agents (session-to-session messaging) |
| Stop sessions from searching past conversations | Agents (past-session search) |
| Let agents share what they learn across kinds | Agent memory (Settings) |
| Let a session see what the other sessions are doing | Nothing to set: every session can ([Let a session see the fleet](../user/fleet/fleet-visibility.md)) |
| Have a session leave you a note about what it noticed | Nothing to set: every session can ([Let a session see the fleet](../user/fleet/fleet-visibility.md)) |
| Clone a private repository | Git hosting |
| Get help investigating an incident | Ops & monitoring |
| Start work from a ticket or an issue | Issue tracker |
| Follow progress while away from the desk | Chat |
| Let the AI use an in-house tool | MCP servers (used by sessions) |
| Drive it from the Claude on my laptop | Outside access → MCP tokens & apps |
| Find out what the tokens went on | Agent usage |
| Find out what my workspace costs to run | Cloud cost |
| I want to know when my workspace was actually running | Running time |
| It memorised something wrong | Agent memory (Memories, Changes); Memory snapshots (History) |
| Change the Java / Node version | Toolchains |
| Run a test suite that needs a database | Databases |
| Show the app you are building to someone | Preview subdomains |
| Get into another server | AWS profiles/SSM |
| Run commands in my Google Cloud projects | Google Cloud |
| Keep code that cannot leave the building | Internal repos |
| Take my settings to another environment | Export / import |
| The environment is broken | Danger zone |

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
| Allowed networks | where members may connect from; the same screen carries the **MCP OAuth** panel, which decides whether outside apps may connect to the tenant through OAuth ([Distribute an MCP server to everyone](../admin/mcp-distribute.md#stop-outside-apps-from-connecting-with-oauth)) |
| Integration OAuth apps | your tenant's own OAuth apps for GitHub / Bitbucket |
| MCP distribution | integration servers handed to every member |
| Branch naming rules | the tenant's default for naming new branches and choosing their base, per repository pattern (a JSON list); members' workspaces pick it up within five minutes. Advice only: a name that does not follow it gets a warning, never a refusal |
| Watch sources | whether members may mark their own MCP servers as watch sources. Block (default) refuses the setting and stops every source in the tenant ([Allow MCP servers as watch sources](../admin/watch-sources.md)) |
| External memory access | whether members may use their Agent Fleet memory from an agent outside Agent Fleet through `/mcp` or the memory-only `/mcp/memory`. Allow (default) lets members opt in; Block withholds the memory tools from the whole tenant ([Allow memory use from outside](../admin/memory-outside.md)) |
| Member defaults | the agent the first-run card names in its "Connect an agent" step (Claude, Codex, opencode, Cursor, Copilot, Kiro, Muse, or none). Members read it whenever the Console loads. It names, never restricts: connecting any other agent completes the step ([Set member defaults](../admin/defaults/members.md)) |
| Audit | who changed what, when |
| Running time | per-member workspace uptime, exportable, plus an hour-by-hour heatmap of the whole tenant |
| Cloud cost | the tenant's cloud spend, per member, only where the deployment runs on AWS ([Read running time and cloud cost](../admin/usage.md#read-cloud-cost)) |
| Inference engine models | opens the model catalogue to add models to the self-hosted engines (only where the deployment administrator granted the tenant model ingest; enabling a model stays with the deployment administrator) |

## Admin modal

The shield-icon **Admin** item, which only a deployment administrator sees ([roles.md](roles.md)). The
rail has three groups: **Tenants**, **Deployment** and **Across tenants**. Some items exist only where
the deployment supports them: **Slots** on the `ecs-ec2` target, **Inference engines** and **API tokens**
wherever the Control Plane serves the engine panel, **Cloud cost** on AWS.

| Section | Configures | Details |
|---|---|---|
| Tenants | creating tenants; each tenant's limits and idle auto-stop, its use of the self-hosted engines and its model-ingest grant, its members | [Install Agent Fleet](../operate/install.md#7-welcome-members) |
| Sign-in method register | approving the sign-in methods tenants define for themselves | [Approve a tenant's own sign-in](../operate/signin/tenant-idp.md) |
| Egress | outbound traffic from workspaces: observe (log-only) or enforce, the allowlist, members' proposals | [Control outbound traffic](../operate/secure/egress.md) |
| MCP sign-in hosts | which client hosts the MCP OAuth server trusts (deployment-wide) | [Operate MCP sign-in](../operate/secure/mcp.md#decide-which-client-hosts-to-trust) |
| Read aloud | the speech engine's mode, and the shared pronunciation dictionary | [Run the read-aloud engine](../operate/engines/read-aloud.md) |
| Appearance | the deployment's colour and label, to tell one deployment from the next | [Tell deployments apart](../operate/run/deployment-label.md) |
| Dev sign-in | the development OIDC IdP switch, for apps on preview subdomains | [Use development sign-in](../operate/secure/dev-signin.md#turn-the-development-oidc-idp-on-or-off) |
| Inference engines | the self-hosted engines: mode, GPU class, model catalogue, state and history | [Run inference engines on your own GPUs](../operate/engines/gpu.md) |
| API tokens | the Hugging Face and Civitai tokens, and the Civitai Red switch, for taking models in | [Take models into the inference engines](../operate/engines/models.md#register-the-tokens) |
| Slots | the `ecs-ec2` slot pool: replacing slots, force-stopping a wedged one | [Replace and stop slots](../operate/aws-ecs/slot-maintenance.md) |
| Sessions, Running time, Cloud cost, Audit, MCP | the tenant views, across every tenant | [admin/](../admin/README.md) |

## Deployment variables

Set before the Control Plane starts. The annotated list is
`deploy/compose/.env.example`, which covers most variables but not all; these are the ones that decide the shape of
everything else:

| Variable | Decides |
|---|---|
| `AF_RUNTIME` | the deployment target; see [deploy-targets.md](deploy-targets.md). Rejected at boot if unknown |
| `AUTH` | how people sign in: `oauth` (the Control Plane's own), `proxy` (an upstream gateway), `dev` (no sign-in, single user). **No default: the Control Plane refuses to start without it** (the native runtime takes `dev`). `dev` on Docker or a cloud runtime also needs `AF_ALLOW_INSECURE_DEV_AUTH=1`, and is refused when `PUBLIC_BASE_URL` is https |
| `DATA_DIR` | where all persistent state lives (the thing to back up) |
| `AF_MASTER_KEY` | the root of at-rest encryption. Lose it and the stored credentials are unrecoverable |
| `AF_KEY_CUSTODIAN` | who holds the at-rest keys: `local` (the default, derived from `AF_MASTER_KEY`) or `kms` (AWS KMS, with `AF_KMS_KEY_ID`; [Put the keys at rest on AWS KMS](../operate/keys/kms.md)). Rejected at boot if unknown or incomplete |
| `AF_WORKSPACE_DEK` | `derived` (the default) or `random`: a KMS-sealed key of its own for each member's home, so a credential store that has moved to it no longer opens with a key derived from `AF_MASTER_KEY` and the Control Plane cannot unwrap it once the KMS key is disabled (copies already handed to the workspace and its runtime still open it). `random` needs `AF_KEY_CUSTODIAN=kms`; turning it off does not undo it ([Give each home its own key](../operate/keys/home-keys.md)) |
| `SUPER_ADMIN_EMAILS` | who is a deployment administrator |
| `PUBLIC_BASE_URL` | the address people reach, and what OAuth callbacks are built from |
| `WS_MEMORY` | the default memory ceiling for a workspace |
| `AF_PREVIEW_DOMAIN` | the parent domain of the preview subdomains (`<workspace>-<port>.<domain>`), which needs a wildcard DNS name and certificate for it. Unset means no preview subdomains, only the Console's own preview path. Set `AF_COOKIE_SECRET` and `PUBLIC_BASE_URL` with it: the Control Plane warns at start when either is missing, because preview sign-in then cannot be trusted or has no Console to return to |
| `AF_DEV_IDP` | `0` locks the development sign-in (an OIDC provider on the Console's origin that a member's app on a preview subdomain can sign in with in place of Google) off, whatever the deployment administrator's switch says. Unset leaves it to that switch, which is on by default. It is only served where `AF_PREVIEW_DOMAIN` and `PUBLIC_BASE_URL` are set |
| `AF_MCP_ENABLED` | `true` serves the `/mcp` endpoint and the memory-only `/mcp/memory` (Personal Access Tokens); anything else, including `1`, leaves it off. The tenant-level memory switch ([Allow memory use from outside](../admin/memory-outside.md)) is a different, narrower switch |
| `AF_MCP_OAUTH` | `1` also makes the Control Plane an OAuth authorization server for `/mcp`, so hosted clients connect by signing the member in. Needs `AF_MCP_ENABLED=true` and an `https` `PUBLIC_BASE_URL`; with any of the three missing the OAuth paths answer 404. Each tenant can still switch it off for itself ([Distribute an MCP server to everyone](../admin/mcp-distribute.md#stop-outside-apps-from-connecting-with-oauth)); [Operate MCP sign-in](../operate/secure/mcp.md#sign-remote-mcp-clients-in-with-oauth) has the rest |
| `AF_GITHUB_WEBHOOK_SECRET` | The webhook secret of this deployment's GitHub App. Set, the Control Plane accepts the App's deliveries at `/webhooks/github` (set the App's webhook URL to `<PUBLIC_BASE_URL>/webhooks/github`) and uses them only to tell running workspaces to check a public repository's pull request sooner; unset, that endpoint does not exist and watches rely on polling alone. Deliveries about private repositories are ignored on this path. Nothing of a delivery is passed on. |
| `AF_MCP_OAUTH_TRUSTED_HOSTS` | the client hosts or `https://host/path` URL prefixes MCP OAuth trusts, comma-separated (a bare host means any path); the default is `https://claude.ai/api/mcp/auth_callback`, `claude.com` and `chatgpt.com`, and `none` leaves loopback clients only. A list saved in the Admin modal (**MCP sign-in hosts**) wins over this variable ([Operate MCP sign-in](../operate/secure/mcp.md#decide-which-client-hosts-to-trust)) |
| `AF_EFS_BACKUP` | not set by hand: on `ecs` / `ecs-ec2`, `update.sh` and `standup.sh` fill it from the ingress stack's `EfsBackup` parameter (`present`, `off` or `missing`), and `missing` raises the Admin screen's "no daily EFS backup" banner ([Upgrade](../operate/run/upgrade.md#the-missing-efs-backup-notice)) |
| `AF_SESSION_IDLE_TIMEOUT`, `AF_WS_IDLE_TIMEOUT`, `AF_BACKGROUND_HOLD_MAX`, `AF_WORKSPACE_START_DEADLINE` | when idle sessions and workspaces are stopped; the defaults are in [limits](limits.md#set-by-the-deployment) and the behaviour in [Use idle stop and force-stop](../operate/run/idle-stop.md#set-the-idle-auto-stop-times) |

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
(`update.sh` overrides only `ImageTag` and the facts it reads from the other stacks:
`EfsBackup`, `WorkspaceSubnets`, `EgressProxyAddr`, `EgressTokenSecretArn` and `EgressArchiveBucket`); see
[`deploy/aws/ecs/cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines).
A LAN engine is reachable from ECS only when the VPC has a route to it (VPN, peering).
