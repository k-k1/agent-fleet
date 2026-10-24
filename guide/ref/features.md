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
| Start a session — agent, model, effort, start mode | member | workspace action bar → Start | [03 Starting sessions](../member/03-starting-sessions.md) |
| Run in a fresh git worktree | member | Start dialog | [06 Repositories and git](../member/06-code.md) |
| Chat mirror — follow and steer a running agent | member | main area | [04 Conversation](../member/04-conversation.md) |
| Answer a question, plan, or permission prompt | member | mirror | [04 Conversation](../member/04-conversation.md); [agents.md](agents.md) for which agents ask |
| Answer a question or plan left by a stopped session | member | mirror, the carried card | [04 Conversation](../member/04-conversation.md#questions-and-plans-left-by-a-stopped-session) |
| Hold follow-ups while a turn runs (Waiting to send) | member | mirror composer → Send ▾ → Add to waiting list, or Alt+Enter | [04 Conversation](../member/04-conversation.md#holding-follow-ups-before-sending-send-queue) |
| Have another session review a plan | member | the plan card | [04 Conversation](../member/04-conversation.md#having-another-session-review-the-plan) |
| Skill / command picker | member | mirror composer | [04 Conversation](../member/04-conversation.md); [agents.md](agents.md) for which agents |
| Live terminal attached to a session | member | main area | [08 Terminal](../member/08-terminal.md) |
| Terminal round-trip time (keystroke lag) | member | terminal pane header | [08 Terminal](../member/08-terminal.md) |
| Ctrl+C / Ctrl+V copy and paste in the terminal | member | terminal | [08 Terminal](../member/08-terminal.md#copy-paste) |
| Resume a stopped session | member | left pane → the session | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md) |
| Home — what waits for you, where you left off, and the first-run setup | member | the empty main area, action bar → Home, or leader `g h` | [01 First day](../member/01-first-day.md) |
| Sessions overview — every running session as a card | member | action bar → Sessions, or leader `g s` | [11 Running many sessions](../member/11-many-sessions.md) |
| Fleet graph — one lane per session, time left to right | member | sessions overview → Graph, or leader `g f` | [11 Running many sessions](../member/11-many-sessions.md#seeing-how-sessions-relate-over-time-the-fleet-graph) |
| Stop every session under a working copy | member | repository / worktree right-click → Stop the sessions below | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md#stopping-and-tidying-up-sessions) |
| Hand a conversation to a new session | member | session ⋯ menu | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md#continuing-elsewhere-handoff-branch-move) |
| Move a session to another agent | member | session ⋯ menu → Move to another agent…, or the usage-limit band in the chat | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md#moving-a-session-to-another-agent-move) |
| Export a conversation as Markdown | member | session ⋯ menu → Export as Markdown | [04 Conversation](../member/04-conversation.md#saving-a-conversation-as-markdown) |
| Fork from a past message | member | mirror, on a past message | [04 Conversation](../member/04-conversation.md); [agents.md](agents.md) for which agents |
| Hand a session to another member | member | handoff proposal card in the chat → Hand off to a member | [12 Working with others](../member/12-collaboration.md#handing-a-session-to-another-member) |
| Share a session read-only | member | Shared sessions | [12 Working with others](../member/12-collaboration.md) |
| Highlights | member | mirror, on a selection | [04 Conversation](../member/04-conversation.md) |
| Changed files | member | strip under the mirror header | [04 Conversation](../member/04-conversation.md) |
| Context usage gauge | member | session header | [09 Agents](../member/09-agents.md) |
| Read an answer in your own language (translation, no turn spent) | member | mirror, under an answer | [04 Conversation](../member/04-conversation.md#reading-an-answer-in-your-own-language-translation) |
| Have a session generate an image (`generate_image`) | member | any session not attached to a studio, by asking | [13 Images](../member/13-images.md#having-a-session-generate-an-image) |
| Start a session from a session (child sessions) — hand a task to another agent, look after what it started | member | Settings → Agents → Session, then any session, by asking | [11 Running many sessions](../member/11-many-sessions.md#starting-sessions-from-a-session-child-sessions) |
| Abort detection and auto-resume | member | automatic | [04 Conversation](../member/04-conversation.md) |

## Working with code

| Feature | Who | Where | Details |
|---|---|---|---|
| Import a repository | member | Repositories → Clone | [06 Repositories and git](../member/06-code.md) |
| Start from an empty folder | member | Repositories → new folder | [06 Repositories and git](../member/06-code.md) |
| Commit graph, diff, stage and commit | member | Commit graph | [06 Repositories and git](../member/06-code.md) |
| Worktrees | member | under each repository | [06 Repositories and git](../member/06-code.md) |
| Use `gh` and `bkt` in a session, already signed in | member | any session | [15 External services](../member/15-external-services.md#use-gh-and-bkt-in-a-session); the list of signed-in CLIs is in [15](../member/15-external-services.md#clis-that-are-already-signed-in) |
| Use `jira` in a session, already signed in | member | any session | [15 External services](../member/15-external-services.md#use-jira-in-a-session) |
| Edit and delete Jira comments (`jira issue comment edit` / `delete`) | member | any session | [15 External services](../member/15-external-services.md#use-jira-in-a-session) |
| Subversion working copies — update, show log, local changes | member | Repositories → an SVN row | [06 Repositories and git](../member/06-code.md#subversion-svn-repositories) |
| File tree and viewer | member | Files | [07 Files](../member/07-files.md) |
| Download a folder as zip | member | Files tree or gallery → right-click → Download as zip | [07 Files](../member/07-files.md#download-a-folder-as-zip) |
| Open a shell from a folder | member | Files tree → right-click a folder → Open shell here | [07 Files](../member/07-files.md#open-a-shell-from-a-folder) |
| Links in rendered Markdown — tickets, file paths, loopback URLs that open the workspace preview | member | mirror, chat, file and document viewers | [07 Files](../member/07-files.md#links-in-rendered-markdown) |
| Menu on file and folder links in replies and documents | member | right-click a link | [Icons, badges and menus](../member/badges-and-menus.md#file-and-folder-links-in-replies-and-documents) |
| Markdown and code editing | member | Files → a file | [07 Files](../member/07-files.md) |
| `.drawio` diagrams | member | Files → a `.drawio` file | [07 Files](../member/07-files.md) |
| PDF and Office document preview | member | Files → that document | [07 Files](../member/07-files.md) |
| Image generation — make pictures in bulk without an agent | member | action bar → Images, or leader `g i` | [13 Images](../member/13-images.md#image-generation) |
| Edit and inpaint — reference pictures, a mask file, instruction-edit models | member | image generation → Advanced → Operation | [13 Images](../member/13-images.md#image-generation) |
| LoRAs with their trigger words, shown before you pick one | member | image generation → LoRAs | [13 Images](../member/13-images.md#image-generation) |
| Image studio — refine the draft by talking to an agent, a person presses generate (edit history, versions, picture history) | member | image generation → Attach an agent | [13 Images](../member/13-images.md#studio-refine-the-draft-by-talking-to-an-agent) |
| Image knowledge notes — four sections per model and per family, in `~/imagegen-knowledge/` | member | image generation → Notes, or the file tree | [13 Images](../member/13-images.md#studio-refine-the-draft-by-talking-to-an-agent) |
| Image gallery — a folder's pictures as cards | member | file tree → "Open in gallery", or a session's "Generated images (N)" | [13 Images](../member/13-images.md#image-gallery) |
| Gallery card menu — copy, rename, delete, jump to the session that made it | member | gallery card → right-click | [Icons, badges and menus](../member/badges-and-menus.md#cards-in-the-image-gallery) |
| Browser pane for a local web app | member | workspace action bar → Preview | [14 Preview and browser](../member/14-preview-browser.md); [browser-pane.md](browser-pane.md) for the contract |
| Preview subdomains (minted on every start) | member | workspace action bar → Preview | [14 Preview and browser](../member/14-preview-browser.md) |
| Sign in with Google on a preview URL (development OIDC IdP) | member | Settings → Preview subdomains | [14 Preview and browser](../member/14-preview-browser.md#signing-in-with-google-on-a-preview-url-development-oidc-idp) |
| Attach to a Chromium the agent owns | member | a link the agent hands you | [14 Preview and browser](../member/14-preview-browser.md) |

## Organising the work

| Feature | Who | Where | Details |
|---|---|---|---|
| Working sets | member | top of the left pane | [11 Running many sessions](../member/11-many-sessions.md) |
| Memo queue | member | Memo queue | [10 Chat and memos](../member/10-chat-memo.md) |
| Work-item inbox — issues, tickets, pull requests | member | Issue tracker | [03 Starting sessions](../member/03-starting-sessions.md); [repos.md](repos.md) for what each provider contributes |
| Scheduled (unattended) runs | member | Schedules | [11 Running many sessions](../member/11-many-sessions.md) |
| Notification centre | member | top bar | [20 Settings](../member/20-settings.md) |
| Inbox — messages and requests between members, with claim, launch and PR links | member | top bar → inbox | [12 Working with others](../member/12-collaboration.md#asking-a-teammate-the-inbox) |
| Inbox recipes — send a prompt with a suggested agent and repository, take it into your memos | member | inbox → New / a card | [12 Working with others](../member/12-collaboration.md#asking-a-teammate-the-inbox) |
| Attach a session to an inbox message, sharing it with the recipients | member | inbox → New | [12 Working with others](../member/12-collaboration.md#asking-a-teammate-the-inbox) |
| Red dot on a session with an unread notification | member | left pane, tabs | [03 Starting sessions](../member/03-starting-sessions.md#reading-state-badges-and-notifications) |
| Engine pills — is the chat / image engine up | member | top bar | [Icons, badges and menus](../member/badges-and-menus.md#the-engine-pills-in-the-top-bar) |
| Assistant chat | member | Assistants | [10 Chat and memos](../member/10-chat-memo.md) |
| Chat bridge — Discord / Slack | member | Settings → Chat | [15 External services](../member/15-external-services.md) |
| Reply suggestions | member | mirror composer | [04 Conversation](../member/04-conversation.md) |
| Deletion lock | member | session ⋯ menu | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md) |
| Cleanup and the trash | member | Clean up | [05 Stopping and tidying up](../member/05-stopping-and-tidying.md) |
| Keyboard system | member | Settings → Keyboard | [02 Console basics](../member/02-console-basics.md) |
| Text-to-speech | member | Settings → Read aloud | [20 Settings](../member/20-settings.md) |

## Personal settings

Every row is under **Settings**; the tab is the "Where".
[settings.md](settings.md) lists the tabs, [20 Settings](../member/20-settings.md)
explains them.

| Feature | Who | Where |
|---|---|---|
| Agent connections | member | Agents |
| Instructions to agents | member | Agent instructions |
| Agent Fleet shared memory management | member | Agent memory ([18](../member/18-agent-memory.md#agent-memory)) |
| Version history of agents' own memory | member | Memory snapshots ([18](../member/18-agent-memory.md#memory-snapshots)) |
| Memory suggestions — finished sessions propose memories for you to approve (off by default) | member | Agent memory → Settings ([18](../member/18-agent-memory.md#settings)) |
| Agent Fleet memory from an agent outside Agent Fleet (off by default) | member | Agent memory → Settings ([18](../member/18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent)) |
| Muse Code contributor mode (off by default) | member | Agents → Muse Code → Behaviour ([09](../member/09-agents.md#muse-code)) |
| Git hosting connections | member | Git hosting |
| Internal repositories | member | Internal repos |
| AWS profiles and SSM | member | AWS profiles/SSM ([16](../member/16-cloud-accounts.md#logging-in-to-another-in-house-host-ssm)) |
| Google Cloud profiles | member | Google Cloud ([16](../member/16-cloud-accounts.md#running-commands-in-google-cloud-as-you-af-gcloud-exec)) |
| Integration servers and tokens | member | MCP servers (used by sessions) / Outside access → MCP tokens & apps |
| Connect a remote MCP client by signing in (OAuth, when the deployment enables it) — Connected apps, Recent calls | member | Outside access → MCP tokens & apps ([19](../member/19-outside-access.md#connecting-a-remote-mcp-client-by-signing-in-oauth)) |
| The "via MCP" badge on input sent through `/mcp` | member | input bubble in the mirror ([19](../member/19-outside-access.md#driving-your-workspace-from-an-external-claude-mcp)) |
| Sign in to a registered MCP server that needs OAuth (Sign in, sign in again, approve extra permission) | member | MCP servers (used by sessions) → Authentication → OAuth ([20](../member/20-settings.md#mcp-servers-used-by-sessions)) |
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
| Members | tenant admin | Members | [admin 01](../admin/01-members.md) |
| Deleting a member's home backups (where the deployment keeps them) | tenant admin | Members → the member | [admin 01](../admin/01-members.md) |
| Sessions across the tenant | tenant admin | Sessions | [admin 02](../admin/02-limits.md) |
| Limits and idle auto-stop | tenant admin (read) | Limits & idle | [admin 02](../admin/02-limits.md) |
| Limit board on / off for the tenant | tenant admin | Limit board | [admin 02](../admin/02-limits.md) |
| Whether this tenant may use the llm / image engines, and whether its administrators may take models in | deployment admin (super_admin) | Tenant settings → Limits | [admin 02](../admin/02-limits.md) |
| Which of the tenant's members may use the llm / image engines | tenant admin | Engine access | [admin 02](../admin/02-limits.md#who-may-use-the-inference-engines-you-can-set-this) |
| Taking engine models in, when the tenant was granted it | tenant admin | Inference engine models | [admin 02](../admin/02-limits.md#taking-engine-models-in-only-if-your-tenant-was-granted-it) |
| Workspace sizing | deployment admin | — | [deploy-targets.md](deploy-targets.md) |
| Sign-in methods and login rules | tenant admin | Sign-in methods / Login rules | [admin 05](../admin/05-access.md#sign-in-methods-and-login-rules) |
| Connection-source restriction | tenant admin | Allowed networks | [admin 05](../admin/05-access.md#allowed-networks) |
| Whether outside apps may connect to the tenant through MCP OAuth | tenant admin | Allowed networks → MCP OAuth | [admin 04](../admin/04-mcp-outside.md#mcp-oauth) |
| Integration app OAuth | tenant admin | Integration OAuth apps | [admin 05](../admin/05-access.md) |
| Branch naming rules — the team's default branch names and base | tenant admin | Branch naming rules | [admin 06](../admin/06-team-defaults.md#branch-naming-rules) |
| Member defaults — the agent new members connect | tenant admin | Member defaults | [admin 06](../admin/06-team-defaults.md#member-defaults) |
| Whether members may use Agent Fleet memory from outside | tenant admin | External memory access | [admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet) |
| Distributing integration servers | tenant admin | MCP distribution | [admin 04](../admin/04-mcp-outside.md) |
| Audit | tenant admin | Audit | [admin 03](../admin/03-audit-usage.md) |
| Running time | tenant admin | Running time | [admin 03](../admin/03-audit-usage.md#running-time-running-time) |
| Cloud cost, per member (AWS only) | tenant admin | Cloud cost | [admin 03](../admin/03-audit-usage.md#cloud-cost-cloud-cost) |

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
| Monitoring integrations | deployment admin | Settings → Ops & monitoring | [member 15](../member/15-external-services.md) |
| Slot pool and instance classes | deployment admin | Admin | [deploy-targets.md](deploy-targets.md) |
| Replacing slots after a launch template change | deployment admin (super_admin) | Admin → Slots | [operator 11](../operate/11-slot-pool.md#ecs-ec2-replacing-slots-after-a-launch-template-change) |
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
