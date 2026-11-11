---
audience: "everyone; the index the other tables hang off"
source_of_truth: "this table for \"does it exist and who can use it\"; the linked page for how"
---

# Features — the catalogue

English | [日本語](features.ja.md)

One row per thing the product does, grouped by where the reader meets it. **Who** is
the smallest role that can use it ([roles.md](roles.md)); **Where** is the screen,
named as the Console names it.

If a feature ships and does not appear here, it is not done
(`docs/CONVENTIONS.md` §8).

## Working with a session

| Feature | Who | Where | Details |
|---|---|---|---|
| Start a session — agent, model, effort, start mode | member | workspace action bar → Start | [Start a session](../user/sessions/start.md) |
| Run in a fresh git worktree | member | Start dialog | [Work in parallel with worktrees](../user/sessions/worktrees.md) |
| Chat mirror — follow and steer a running agent | member | main area | [Reply to a session](../user/sessions/reply.md) |
| Answer a question, plan, or permission prompt | member | mirror | [Reply to a session](../user/sessions/reply.md); [agents.md](agents.md) for which agents ask |
| Answer a question or plan left by a stopped session | member | mirror, the carried card | [Reply to a session](../user/sessions/reply.md#answer-the-cards-a-stopped-session-left) |
| Hold follow-ups while a turn runs (Waiting to send) | member | mirror composer → Send ▾ → Add to waiting list, or Alt+Enter | [Hold what you send, stop a turn](../user/sessions/queue.md#hold-follow-ups-in-the-waiting-list) |
| Have another session review a plan | member | the plan card | [Comment on a plan, have it reviewed](../user/sessions/plans.md#have-another-session-review-it) |
| Skill / command picker | member | mirror composer | [Call a skill or a command](../user/sessions/skills.md); [agents.md](agents.md) for which agents |
| Live terminal attached to a session | member | main area | [Use the terminal](../user/files/terminal.md) |
| Terminal round-trip time (keystroke lag) | member | terminal pane header | [Use the terminal](../user/files/terminal.md) |
| Ctrl+C / Ctrl+V copy and paste in the terminal | member | terminal | [Use the terminal](../user/files/terminal.md#copy-and-paste) |
| Resume a stopped session | member | left pane → the session | [Stop and resume a session](../user/sessions/stop-resume.md) |
| Home — what waits for you, where you left off, at a glance, and the first-run setup | member | the empty main area, action bar → Home, or leader `g h` | [Find your way around the screen](../user/start/console-tour.md#home) |
| Sessions overview — every running session as a card | member | action bar → Sessions, or leader `g s` | [See every running session](../user/fleet/overview.md) |
| Fleet graph — one lane per session, time left to right | member | sessions overview → Graph, or leader `g f` | [See how sessions relate over time](../user/fleet/graph.md) |
| Stop every session under a working copy | member | repository / worktree right-click → Stop the sessions below | [Stop and resume a session](../user/sessions/stop-resume.md#stop-every-session-in-one-working-copy) |
| Hand a conversation to a new session | member | session ⋯ menu | [Hand off to another session or agent](../user/sessions/handoff.md#choose-a-way) |
| Move a session to another agent | member | session ⋯ menu → Move to another agent…, or the usage-limit band in the chat | [Hand off to another session or agent](../user/sessions/handoff.md#move-to-another-agent) |
| Export a conversation as Markdown | member | session ⋯ menu → Export as Markdown | [Save a conversation as Markdown](../user/sessions/export.md) |
| Fork from a past message | member | mirror, on a past message | [Branch from a past message](../user/sessions/branch-message.md); [agents.md](agents.md) for which agents |
| Hand a session to another member | member | session ⋯ menu or handoff proposal card → Hand off to a member | [Hand a session to a member](../user/team/hand-over.md) |
| Share a session read-only | member | Shared sessions | [Share a conversation](../user/team/share.md) |
| Highlights | member | mirror, on a selection | [Reply to a session](../user/sessions/reply.md#highlight-a-line-in-the-conversation) |
| Changed files | member | strip under the mirror header | [See what a session changed](../user/sessions/changes.md) |
| Context usage gauge | member | session header | [Check prices and usage](../user/agents/usage.md#see-how-long-the-conversation-is) |
| Read an answer in your own language (translation, no turn spent) | member | mirror, under an answer | [Read an answer in your own language](../user/sessions/translate.md) |
| Have a session generate an image (`generate_image`) | member | any session not attached to a studio, by asking | [Have a session make images](../user/images/from-session.md) |
| Start a session from a session (child sessions) — hand a task to another agent, look after what it started | member | Settings → Agents → Session, then any session, by asking | [Start child sessions from a session](../user/fleet/child-sessions.md) |
| Abort detection and auto-resume | member | automatic | [04 Conversation](../member/04-conversation.md) |

## Working with code

| Feature | Who | Where | Details |
|---|---|---|---|
| Import a repository | member | Repositories → Clone | [Clone a repository](../user/code/clone.md) |
| Start from an empty folder | member | Repositories → new folder | [Clone a repository](../user/code/clone.md#start-from-nothing) |
| Commit graph, diff, stage and commit | member | Commit graph | [Commit in the commit graph](../user/code/commit.md) |
| Worktrees | member | under each repository | [Launch a session from a repository](../user/code/launch-from-repo.md) |
| Use `gh` and `bkt` in a session, already signed in | member | any session | [Use the GitHub CLI](../user/integrations/github-cli.md), [Use the Bitbucket CLI](../user/integrations/bitbucket-cli.md); the list of signed-in CLIs is in [Commands](cli/README.md#clis-that-are-already-signed-in) |
| Use `jira` in a session, already signed in | member | any session | [Use Jira](../user/integrations/jira.md) |
| Use `confluence` in a session, already signed in (the Jira connection) | member | any session | [Use Confluence](../user/integrations/confluence.md) |
| Edit and delete Jira comments (`jira issue comment edit` / `delete`) | member | any session | [Use Jira](../user/integrations/jira.md#ask-an-agent) |
| Subversion working copies — update, show log, local changes | member | Repositories → an SVN row | [Use a Subversion repository](../user/code/svn.md) |
| File tree and viewer | member | Files | [See and download files](../user/files/browse.md) |
| Download a folder as zip | member | Files tree or gallery → right-click → Download as zip | [See and download files](../user/files/browse.md#download-a-folder-as-zip) |
| Open a shell from a folder | member | Files tree → right-click a folder → Open shell here | [See and download files](../user/files/browse.md#open-a-shell-from-a-folder) |
| Links in rendered Markdown — tickets, file paths, loopback URLs that open the workspace preview | member | mirror, chat, file and document viewers | [View documents and diagrams](../user/files/view.md#links-in-documents) |
| Menu on file and folder links in replies and documents | member | right-click a link | [View documents and diagrams](../user/files/view.md#links-in-documents) |
| Markdown and code editing | member | Files → a file | [Edit a file](../user/files/edit.md) |
| `.drawio` diagrams | member | Files → a `.drawio` file | [View documents and diagrams](../user/files/view.md) |
| PDF and Office document preview | member | Files → that document | [View documents and diagrams](../user/files/view.md) |
| Image generation — make pictures in bulk without an agent | member | action bar → Images, or leader `g i` | [Generate images](../user/images/generate.md) |
| Edit and inpaint — reference pictures, a mask file, instruction-edit models | member | image generation → Advanced → Operation | [Generate images](../user/images/generate.md#edit-a-picture-you-have) |
| LoRAs with their trigger words, shown before you pick one | member | image generation → LoRAs | [Generate images](../user/images/generate.md#write-the-prompt) |
| Image studio — refine the draft by talking to an agent, a person presses generate (edit history, versions, picture history) | member | image generation → Attach an agent | [Finish an image in the studio](../user/images/studio.md) |
| Image knowledge notes — four sections per model and per family, in `~/imagegen-knowledge/` | member | image generation → Notes, or the file tree | [Finish an image in the studio](../user/images/studio.md#grow-the-knowledge-notes) |
| Image gallery — a folder's pictures as cards | member | file tree → "Open in gallery", or a session's "Generated images (N)" | [Use the image gallery](../user/images/gallery.md) |
| Gallery card menu — copy, rename, delete, jump to the session that made it | member | gallery card → right-click | [Use the image gallery](../user/images/gallery.md#act-on-a-card) |
| Browser pane for a local web app | member | workspace action bar → Preview | [See a web app you started](../user/preview/browser-pane.md); [browser-pane.md](browser-pane.md) for the contract |
| Preview subdomains (minted on every start) | member | workspace action bar → Preview | [Use preview subdomains](../user/preview/subdomains.md) |
| Sign in with Google on a preview URL (development OIDC IdP) | member | Settings → Preview subdomains | [Try Google sign-in on a preview](../user/preview/dev-signin.md) |
| Attach to a Chromium the agent owns | member | a link the agent hands you | [Operate a browser the agent opened](../user/preview/agent-browser.md) |

## Organising the work

| Feature | Who | Where | Details |
|---|---|---|---|
| Working sets | member | top of the left pane | [Narrow the view with working sets](../user/fleet/working-sets.md) |
| Memo queue | member | Memo queue | [10 Chat and memos](../member/10-chat-memo.md) |
| Work-item inbox — issues, tickets, pull requests | member | Issue tracker | [Start from an issue or a ticket](../user/sessions/from-issues.md); [repos.md](repos.md) for what each provider contributes |
| Scheduled (unattended) runs | member | Schedules | [Run at a set time](../user/fleet/schedules.md) |
| Notification centre | member | top bar | [20 Settings](../member/20-settings.md) |
| Inbox — messages and requests between members, with claim, launch and PR links | member | top bar → inbox | [Ask a teammate through the inbox](../user/team/inbox.md) |
| Inbox recipes — send a prompt with a suggested agent and repository, take it into your memos | member | inbox → New / a card | [Ask a teammate through the inbox](../user/team/inbox.md) |
| Attach a session to an inbox message, sharing it with the recipients | member | inbox → New | [Ask a teammate through the inbox](../user/team/inbox.md) |
| Red dot on a session with an unread notification | member | left pane, tabs | [Read a session's state](../user/sessions/status.md#the-red-dot) |
| Work-item links on a session row — its branch's pull request and the tickets it is linked to | member | left pane, session row | [Start from an issue or a ticket](../user/sessions/from-issues.md#how-sessions-and-tickets-are-linked); [repos.md](repos.md) for which providers |
| Pull request notifications — ready to merge, merged, closed | member | Settings → Notifications | [20 Settings](../user/settings/notifications.md) |
| A session watching a pull request — told when checks finish, conflicts, merges | member | (agent tool `watch`) | [Tell a session about its pull request's review](../user/fleet/pr-feedback.md#a-session-watches-a-pull-request-itself) |
| Not responding badge — the workspace is up but its Agent does not answer | member | beside the workspace power button | [Fix a workspace that will not start or has stopped](../user/troubleshooting/workspace.md#not-responding-appears-beside-the-power-button) |
| Engine pills — is the chat / image engine up | member | top bar | [Use lcpp](../user/agents/lcpp.md) |
| Assistant chat | member | Assistants | [10 Chat and memos](../member/10-chat-memo.md) |
| Chat bridge — Discord / Slack | member | Settings → Chat | [Connect Discord and Slack](../user/integrations/chat-bridge.md) |
| Reply suggestions | member | mirror composer | [Reply to a session](../user/sessions/reply.md#use-reply-suggestions) |
| Deletion lock | member | session ⋯ menu | [Clear away finished sessions](../user/sessions/cleanup.md#lock-something-against-deletion) |
| Cleanup and the trash | member | Clean up | [Clear away finished sessions](../user/sessions/cleanup.md#tidy-up-in-bulk-cleanup) |
| Keyboard system | member | Settings → Keyboard | [Use the keyboard and the command palette](../user/start/shortcuts.md) |
| Text-to-speech | member | Settings → Read aloud | [20 Settings](../member/20-settings.md) |
| Skills views — each agent's skills, read-only: personal (Settings) and a repository's (project tree → Skills…) | member | Settings → Skills; repository row menu → Skills… | [Bring your Claude Code setup](../user/agents/bring-claude-setup.md#check-your-skills) |

## Personal settings

Every row is under **Settings**; the tab is the "Where".
[settings.md](settings.md) lists the tabs, [20 Settings](../member/20-settings.md)
explains them.

| Feature | Who | Where |
|---|---|---|
| Agent connections | member | Agents |
| Instructions to agents | member | Agent instructions |
| Agent Fleet shared memory management | member | Agent memory ([Read and correct agent memory](../user/memory/overview.md)) |
| Version history of agents' own memory | member | Memory snapshots ([Restore memory from a snapshot](../user/memory/snapshots.md)) |
| Agent Fleet memory from an agent outside Agent Fleet (off by default) | member | Agent memory → Settings ([Use memory from an external agent](../user/memory/external.md)) |
| Muse Code contributor mode (off by default) | member | Agents → Muse Code → Behaviour ([09](../user/agents/muse.md#behavior)) |
| Git hosting connections | member | Git hosting |
| Internal repositories | member | Internal repos |
| AWS profiles and SSM | member | AWS profiles/SSM ([Use AWS SSO](../user/integrations/aws-sso.md), [Log in to EC2 with SSM](../user/integrations/aws-ssm.md)) |
| Google Cloud profiles | member | Google Cloud ([Use Google Cloud](../user/integrations/gcloud.md)) |
| Integration servers and tokens | member | MCP servers (used by sessions) / Outside access → MCP tokens & apps |
| Connect a remote MCP client by signing in (OAuth, when the deployment enables it) — Connected apps, Recent calls | member | Outside access → MCP tokens & apps ([Connect a remote MCP client by signing in](../user/outside/oauth-client.md)) |
| The "via MCP" badge on input sent through `/mcp` | member | input bubble in the mirror ([Drive your workspace from an external Claude](../user/outside/external-claude.md#how-an-instruction-looks-to-the-session)) |
| Sign in to a registered MCP server that needs OAuth (Sign in, sign in again, approve extra permission) | member | MCP servers (used by sessions) → Authentication → OAuth ([Add an MCP server to your sessions](../user/integrations/mcp-servers.md#sign-in-to-an-oauth-server)) |
| Issue-tracker connections | member | Issue tracker |
| Usage | member | Agent usage |
| Limit board: publish where you stand on usage limits, and see what teammates publish to you | member | Limit board |
| Cloud cost | member | Cloud cost |
| AI assistance (per-feature on/off, agent, model) | member | AI assistance |
| Display, language and keys | member | Display / Keyboard |
| Export / import settings | member | Export / import |
| Toolchains | member | Toolchains |
| Databases (Postgres / MySQL — install, start, stop, reset) | member | Toolchains |
| Preview-subdomain settings | member | Preview subdomains (only where they are issued) |
| Destructive actions | member | Danger zone (Recreate, Clean home; only where the deployment can perform them — [deploy-targets.md](deploy-targets.md)) |

## Tenant administration

Every row is under **Tenant settings**. [admin/](../admin/README.md) is the shelf;
[roles.md](roles.md) says what is read-only for whom.

| Feature | Who | Where | Details |
|---|---|---|---|
| Members | tenant admin | Members | [Check on your members](../admin/members/roster.md) |
| Deleting a member's home backups (where the deployment keeps them) | tenant admin | Members → the member | [Remove a member](../admin/members/remove.md#4-delete-the-backups) |
| Sessions across the tenant | tenant admin | Sessions | [See how resources are used](../admin/resources/overview.md#see-the-sessions-running-now) |
| Limits and idle auto-stop | tenant admin (read) | Limits & idle | [Set session limits](../admin/resources/session-limits.md#check-the-tenant-wide-limits), [Stop workspaces left idle](../admin/resources/idle-stop.md) |
| Limit board on / off for the tenant | tenant admin | Limit board | [Set session limits](../admin/resources/session-limits.md#decide-whether-to-use-the-limit-board) |
| Whether this tenant may use the llm / image engines, and whether its administrators may take models in | deployment admin (super_admin) | Tenant settings → Limits | [Choose who may use the inference engines and which models](../admin/resources/engines.md) |
| Which of the tenant's members may use the llm / image engines | tenant admin | Engine access | [Choose who may use the inference engines and which models](../admin/resources/engines.md#choose-the-members-who-may-use-them) |
| Taking engine models in, when the tenant was granted it | tenant admin | Inference engine models | [Choose who may use the inference engines and which models](../admin/resources/engines.md#take-models-in) |
| Workspace sizing | deployment admin | — | [deploy-targets.md](deploy-targets.md) |
| Sign-in methods and login rules | tenant admin | Sign-in methods / Login rules | [Choose how people sign in](../admin/access/signin-methods.md) |
| Connection-source restriction | tenant admin | Allowed networks | [Limit the networks people sign in from](../admin/access/networks.md) |
| Whether outside apps may connect to the tenant through MCP OAuth | tenant admin | Allowed networks → MCP OAuth | [Distribute an MCP server to everyone](../admin/mcp-distribute.md#stop-outside-apps-from-connecting-with-oauth) |
| Integration app OAuth | tenant admin | Integration OAuth apps | [Choose the OAuth apps for integrations](../admin/access/oauth-apps.md) |
| Branch naming rules — the team's default branch names and base | tenant admin | Branch naming rules | [Set the branch naming rules](../admin/defaults/branch-rules.md) |
| Member defaults — the agent new members connect | tenant admin | Member defaults | [Set member defaults](../admin/defaults/members.md) |
| Whether members may use Agent Fleet memory from outside | tenant admin | External memory access | [Allow memory use from outside](../admin/memory-outside.md) |
| Distributing integration servers | tenant admin | MCP distribution | [Distribute an MCP server to everyone](../admin/mcp-distribute.md) |
| Audit | tenant admin | Audit | [Read the audit log](../admin/audit.md) |
| Running time | tenant admin | Running time | [Read running time and cloud cost](../admin/usage.md#read-running-time) |
| Cloud cost, per member (AWS only) | tenant admin | Cloud cost | [Read running time and cloud cost](../admin/usage.md#read-cloud-cost) |

## Operating a deployment

| Feature | Who | Where | Details |
|---|---|---|---|
| Deployment targets | deployment admin | before start | [deploy-targets.md](deploy-targets.md) |
| Install, upgrade, back up, restore | deployment admin | a shell | [operator 02](../operate/02-install.md) / [03](../operate/03-run.md) |
| Ingress, TLS and sign-in providers | deployment admin | a shell | [operator 04](../operate/04-secure.md) |
| Audit log and egress control | deployment admin | Admin | [operator 04](../operate/04-secure.md#operating-egress-control) |
| Which client hosts MCP OAuth trusts | deployment admin (super_admin) | Admin → MCP sign-in hosts | [operator 04](../operate/04-secure.md#trusted-client-hosts-deployment-wide-superadmin-only) |
| The development OIDC IdP for preview URLs | deployment admin (super_admin) | Admin → Dev sign-in | [operator 04](../operate/04-secure.md#development-sign-in-dev-oidc-idp-superadmin-only) |
| Keys at rest on AWS KMS, and a key of its own for each home | deployment admin | a stack parameter, then a shell | [operator 12](../operate/12-keys.md) |
| The deployment's colour and label (Appearance) | deployment admin (super_admin) | Admin → Appearance | [operator 03](../operate/03-run.md#telling-two-deployments-apart-colour-and-label) |
| Pausing an idle deployment | deployment admin | a shell | [operator 03](../operate/03-run.md#pausing-an-idle-deployment) |
| Monitoring integrations | deployment admin | Settings → Ops & monitoring | [Connect monitoring tools](../user/integrations/ops-monitoring.md) |
| Slot pool and instance classes | deployment admin | Admin | [deploy-targets.md](deploy-targets.md) |
| Replacing slots after a launch template change | deployment admin (super_admin) | Admin → Slots | [operator 11](../operate/11-slot-pool.md#ecs-ec2-replacing-outdated-slots-new-ami-or-launch-template-change) |
| Force-stopping a wedged slot | deployment admin (super_admin) | Admin → Slots → Stop (force) | [operator 11](../operate/11-slot-pool.md#ecs-ec2-stopping-a-wedged-slot) |
| The inference engines' GPU class | deployment admin (super_admin) | Admin → Inference engines | [operator 07](../operate/07-engines.md#the-gpu-instance-class) |
| Taking a model in — search, plan card, one press | deployment admin (super_admin), or a tenant admin who was allowed | Admin → Inference engines → Model catalogue | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| The S3 bucket as the ledger of what is actually held | deployment admin (super_admin) | Admin → Inference engines → Model catalogue → Bucket | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| Model names, example images and family categories in the catalogue | deployment admin (super_admin), or a tenant admin who was allowed | Admin → Inference engines → Model catalogue → Registered | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| A repository's quantisations, with what fits the GPU class | deployment admin (super_admin), or a tenant admin who was allowed | Admin → Inference engines → Model catalogue → Registered | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| Hugging Face and Civitai tokens for gated downloads | deployment admin (super_admin) | Admin → API tokens | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| Civitai Red (the NSFW sister domain) as a search source | deployment admin (super_admin), where the deployment was given it | Admin → API tokens | [operator 07](../operate/07-engines.md#taking-models-in-and-what-that-has-to-do-with-tenants) |
| The speech engine and the shared pronunciation dictionary | deployment admin (super_admin) | Admin → Read aloud | [operator 07](../operate/07-engines.md#read-aloud-the-speech-engine-and-the-shared-dictionary) |
| Image generation on a ComfyUI of your own | deployment admin (super_admin) | a shell, then Admin → Inference engines | [operator 08](../operate/08-image-engine.md) |
| Borrowing another deployment's llm / image engines | deployment admin (super_admin) | a shell, plus a membership on the far deployment | [operator 09](../operate/09-borrowed-engine.md) |
| Chat inference on a llama.cpp of your own | deployment admin (super_admin) | a shell, then Admin → Inference engines | [operator 10](../operate/10-llm-lan.md) |
| The user guide inside containers | — | automatic | [roles.md](roles.md) |
