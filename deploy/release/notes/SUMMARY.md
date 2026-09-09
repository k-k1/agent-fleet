# Feature and fix index

Every published release, newest first, with each new feature and each fix on one
line. The per-version notes answer "is there anything in here for me?"; this file
answers "when did X ship?" and "which release fixed Y?" without opening 30 files.

Each line starts with the area it belongs to — **[preview]**, **[ecs-ec2]**,
**[mirror]** — so a fix can be traced back to the feature it repairs, which is not
otherwise visible once the bullets are one line long.

Keeping it current:

- Add the new version's section at the top **as part of publishing** (see the steps
  in [README.md](README.md)); `release-gate` fails when a ledger row has no section
  here. Japanese lives in [SUMMARY.ja.md](SUMMARY.ja.md) and moves with it.
- One line per item, condensed from that version's notes. Upgrade steps stay in the
  notes and do not come here.
- The **CLI pins** line lists only the agent CLIs whose pin moved in that version;
  no line means nothing moved. Take the values from the build commit rather than
  from the prepared notes — a pin bump can land between writing them and publishing:
  `git show <build-commit>:workspace/Dockerfile | grep -E '^ARG (CLAUDE_CODE|OPENCODE|CODEX|COPILOT|AGY|CURSOR|KIRO|RTK)_VERSION='`
  diffed against the previous version's build commit.
- Only versions in [index.tsv](index.tsv) belong here. Notes exist for a couple of
  versions that were prepared and then never published (0.8.1, 0.12.5); the ledger,
  not the presence of a file, says what shipped.

---

## [0.17.0](0.17.0.md) — 2026-09-09

**New / Improved**

- **[engines]** The fleet runs its own inference engines on a GPU bought on demand: `llamacpp/<model>` in opencode's launch menu (`llm`) and `generate_image` served by the deployment (`image`); disabled / always-on / on-demand in Settings › Admin (ECS only, opt-in)
- **[engines]** Models are ingested from Hugging Face and swapped from the Console, with no CloudFormation run
- **[engines]** The engines panel shows start time, automatic stop time, recent demand, the model in VRAM and 14 days of occupancy
- **[sessions]** A session can be told to stop once it is done: it finishes the running turn and then halts (⋯ menu, agents, operator)
- **[schedules]** A scheduled run can stop its session afterwards, instead of leaving the workspace up until the idle timeout
- **[sessions]** A session can observe the fleet it is part of — another session's state, its own context usage, reading and adding memos — and nothing that acts (off by default, Settings › Agents)
- **[image generation]** Antigravity added as a second provider (a different plan, honours the aspect ratio) and tried first; a request can name the provider, and an automatic one falls through when a plan is out of quota
- **[mirror › skills]** Claude's bundled skills are in the skill picker, folded below your own
- **[cost]** The shared-cost card breaks inference engines and the speech engine out of the EC2 / ECS lumps
- **[sessions]** Tabs of sessions archived elsewhere (cleanup screen, another device, operator, agent) close themselves
- **[auth]** After re-authenticating, the card says so and the session resumes on its own
- **[repos]** A worktree of a repository with large submodules is seeded from the local clone instead of re-downloading them
- **[agents]** Antigravity is usable on hosts whose kernel has withdrawn RDRAND
- **[sessions]** Agent Fleet's own tool descriptions cost each session less context

**Fixed**

- **[mirror]** In a long conversation the work process reopened by itself and the position jumped, on scroll and on loading older messages
- **[question cards]** A half-written free-text answer was lost when the session stopped or resumed
- **[viewer]** The font-size setting did not reach Markdown; dragging the minimap selected the text it passed
- **[mirror]** A path could be swallowed by a commit link and stop opening (generated image paths in particular)
- **[notifications]** A notification with no session to open said the session could not be found
- **[ecs-ec2]** A launch that changed the slot type said it would never start, half a minute before it started
- **[usage]** Codex's usage chip ignored image generation
- **[agents]** opencode's launch menu listed `amazon-bedrock/*` models no workspace can reach

## [0.16.0](0.16.0.md) — 2026-09-07

**CLI pins** — Claude Code 2.1.263

**New / Improved**

- **[image generation]** `generate_image` for agents without their own image tool (claude/opencode etc.; off by default, enabled per member)
- **[image generation / mirror]** Generated images appear in the conversation as cards with thumbnails (claude / opencode / copilot)
- **[text-to-speech]** VOICEVOX runs on demand (off / always-on / on-demand; Polly reads until it is up)
- **[settings › machine]** New *Machine* tab: measured values next to the deployment's declared ones, plus an hour of resource history
- **[composer]** `Ctrl`+`R` searches everything you have sent before
- **[settings › toolchains]** Node 26 can be installed
- **[shared files]** Shared images open in a lightbox at full size, with zoom and pan
- **[mirror performance]** Long conversations no longer get slower to open (incremental reads); the shared-image panel fetches thumbnails
- **[settings › toolchains]** Node 18 removed from the list (EOL)

**Fixed**

- **[mobile]** The workspace bar overflowed the screen width, which killed horizontal swiping entirely
- **[question cards]** A half-made choice was lost when switching tabs
- **[mobile / selection]** The selection colour chips hid behind the browser's own copy/share menu
- **[session state]** An agent process that had already exited was reported as running

## [0.15.1](0.15.1.md) — 2026-09-05

**New / Improved**

- **[distribution / ecs-ec2]** The workspace image is published for arm64, so Graviton slots run on the official image (code identical to 0.15.0)

**Fixed** — none

## [0.15.0](0.15.0.md) — 2026-09-05

**CLI pins** — Claude Code 2.1.261 / Codex 0.153.4 / OpenCode 1.18.29 / Copilot 1.0.83 / Antigravity 1.1.26 / Cursor 2026.09.02 / Kiro 2.21.1 / rtk 0.48.0

**New / Improved**

- **[left pane › FILES]** The file list refreshes itself and marks rows added since you last looked
- **[command palette]** Sessions are now the first mode (`Ctrl`/`⌘`+`P`)
- **[settings › AI assist]** Titles, reply suggestions, branch names and file-edit proposals moved to their own tab, each switchable off
- **[settings › toolchains]** node can be installed
- **[workspace image]** Rebased on Debian 13 (trixie): newer chromium, git-delta added
- **[workspace / arch change]** `pip --user`, `uv` tools and global npm packages are reinstalled for the new architecture at start; what cannot be repaired is named in the notification centre
- **[managed runtime]** The Codex / OpenCode daemons no longer start without a login, and are shut down when nobody uses them
- **[start flow]** The first prompt typed into *Start work* survives closing the dialog
- **[i18n]** Agent sign-in errors follow the Console language
- **[left pane › worktree]** Directory name in the row tooltip; duplicate entries removed from the context menu

**Fixed**

- **[session-to-session messages]** With messaging on, Claude sessions could fail to start a turn (incomplete tool schema)
- **[composer › attachments]** Pasted and dropped images were being discarded
- **[file viewer]** Reading position is kept across tabs; the raw terminal no longer flashes before the mirror
- **[question cards]** Cancelling a card could leave the composer unable to send
- **[mobile]** Pinch-zooming an image and panning switched to a different session
- **[ECS deploy]** `30-ingress.yaml` passed CloudFormation's 51,200-byte limit and every ECS deploy path was broken
- **[account menu]** Admin entries disappeared during a backend outage and did not return until reload
- **[start flow]** A managed session that could not start because the agent was not logged in now says so
- **[transcript › marks]** The mark picker did not appear for selections crossing a line break
- **[notifications]** The "conversation is gone" notification was a dead end
- **[agent memory]** A failed secret scan looked identical to "no secrets found"
- **[codex / shared files]** Generated images can be opened from the shared-file preview
- **[ECS ops scripts]** Teardown states why a bucket could not be emptied; stand-up preflight compares the saved parameters with the current template; values that exist only as stack Outputs are saved

## [0.14.2](0.14.2.md) — 2026-09-01

**CLI pins** — Claude Code 2.1.252 / Codex 0.152.0 / Antigravity 1.1.23 / Cursor 2026.08.31 / Kiro 2.20.2

**New / Improved**

- **[usage › uptime]** Uptime is drawn as a 24h × date heatmap (occupancy, not money; recording starts with this version)
- **[settings › preview]** The preview-subdomain settings became their own screen

**Fixed**

- **[control plane / database]** On deployments where the database rotates its own password, every request started returning 500 about a week in — silently. Added `/readyz` and an email alarm
- **[session state]** A session running subagents looked like it was waiting for input
- **[handoff / notifications]** A received handoff could not be started from where the notification led
- **[issue tracker]** Branch names from a ticket keep the issue key's capitalisation

## [0.14.1](0.14.1.md) — 2026-09-01

**New / Improved**

- **[preview / ECS]** `PreviewHostedZoneId` puts the preview certificate and DNS records in a separate zone

**Fixed**

- **[workspace settings / Postgres]** Saving workspace settings always failed; reads returned defaults, so the screen looked fine
- **[guide delivery]** The user guide would not open from inside a workspace
- **[preview]** The settings show the current URL, and re-issuing always says what it did
- **[settings › tool versions]** OpenCode and Copilot could show `(timeout)`

## [0.14.0](0.14.0.md) — 2026-08-31

**CLI pins** — Claude Code 2.1.251 / Codex 0.151.0 / Copilot 1.0.82

**New / Improved**

- **[preview]** Each workspace start issues a dedicated preview URL at `https://{slug}-{port}.{domain}/` — at the root, which is what React / Next.js and Spring Boot need
- **[file pane]** PDFs render (continuous scroll, zoom, page numbers; Japanese without embedded fonts is fine)
- **[file pane]** Simple preview for Word, Excel, PowerPoint, OpenDocument and EPUB
- **[repo import]** A new folder is initialised as a git repository and leads straight into *Start work*
- **[usage]** Sessions get an estimated cost from their tokens; the tab is now *Agent usage*
- **[terminal]** The header shows the round trip to the workspace
- **[terminal]** Input got faster (Esc was always half a second late; keystrokes waited on a database write)
- **[documentation]** Split by reader: English guide, Japanese guide, README, developer material
- **[tabs]** Right-clicking a tab opens the same menu as the session row
- **[mirror]** Long tool traces and thinking can be collapsed from their end as well

**Fixed**

- **[question cards]** A cancelled question reappeared as a card where nothing worked
- **[plans]** An approved plan could get a *rejected* badge
- **[sharing]** People you shared with saw nothing while a question was open
- **[session-to-session messages]** Messages arriving over claude's own path reached the CLI but not the screen
- **[worktree / start flow]** A session in a fresh working copy could start from a weeks-old state
- **[control plane / SQLite]** Columns added later were missing on a freshly built database
- **[composer]** The transcript drifted off the newest message on every keystroke
- **[opencode]** The model list was ordered differently each time the picker opened
- **[schedules / ecs-ec2]** Scheduled runs silently did not fire (only 90 seconds to wake a stopped workspace)
- **[ecs-ec2 › golden]** Re-baking golden could leave an ECS service and a 50 GiB disk behind
- **[display]** Assistant chat tab names; the issue-tracker row font size

## [0.13.1](0.13.1.md) — 2026-08-28

**CLI pins** — Claude Code 2.1.250 / OpenCode 1.18.25

**New / Improved**

- **[issue tracker]** Bitbucket queries are assembled from choices instead of written by hand; duplicate tickets collapse to one row
- **[naming]** The rail and the settings agree on *Issue tracker*

**Fixed**

- **[claude state detection]** A claude session writing its answer was shown as waiting for input
- **[handoff / start flow]** Starting from a handoff card could fail with "title is too long"
- **[issue tracker]** The same ticket appeared once per saved query
- **[browser pane]** The pane could stay black and never paint
- **[display]** The mirror's chat/terminal switch follows the top bar colour; the query source is readable in dark themes

## [0.13.0](0.13.0.md) — 2026-08-28

**CLI pins** — Claude Code 2.1.247 / Codex 0.150.1 / Copilot 1.0.81 / Antigravity 1.1.22 / Cursor 2026.08.25 / Kiro 2.20.1 / rtk 0.46.0

**New / Improved**

- **[issue tracker]** Work items (GitHub issues/PRs, Jira issues, Bitbucket PRs) list in the left pane, and work starts from one of them
- **[issue tracker / connections]** GitHub / Jira / Bitbucket connect per tenant; settings gained an *Issue tracker* tab
- **[issue tracker]** Reports can be written back to the ticket — Agent Fleet drafts the facts, you write the text and press post
- **[ecs-ec2]** Workspaces run with a memory cap, so one workspace can no longer take the box down with it

**Fixed**

- **[ecs-ec2]** A workspace could become permanently unstartable, returning 504 every time (the box looked healthy from outside, the OS inside was dead)
- **[mobile / session state]** A running turn was shown as waiting for input on narrow screens
- **[session-to-session messages]** Messages from another session looked like your own
- **[start flow / models]** The reason for a failure on a retired model was buried in the list of available ones
- **[cursor]** Adding `CI` to the environment left Cursor sessions as a dead banner-only screen

## [0.12.4](0.12.4.md) — 2026-08-26

**Fixed**

- **[ecs-ec2]** Upgrading to 0.12.3 made every pre-existing workspace unstartable — the "one task at a time" pin added in 0.12.3 conflicts with AZ rebalancing

## [0.12.3](0.12.3.md) — 2026-08-26

**CLI pins** — Claude Code 2.1.246 / OpenCode 1.18.23 / Antigravity 1.1.21 / Kiro 2.19.2

**New / Improved**

- **[repo import]** Imports became jobs you can watch, instead of "the folder exists, so it worked"
- **[settings]** Export and import your settings as one file (connection tokens and API keys are always excluded)
- **[ecs-ec2 / networking]** A deployment keeps its outbound IP (the NAT gateway's Elastic IP)
- **[ecs-ec2 › slot pool]** Slots left stopped are terminated so their root volumes stop billing
- **[tenant quotas]** The settings warn when the tenants' total exceeds the pool, and negative limits are rejected
- **[ecs-ec2]** Workspaces start faster (task definitions are reused)

**Fixed**

- **[ecs-ec2 › slot pool]** A full pool could leave a workspace permanently unable to start
- **[usage chip]** Accounts close to their limit could still read 0%
- **[cost allocation]** AWS Organizations member accounts kept showing "no cost is attributed to anyone" while the numbers were on screen
- **[display / CJK]** Enclosed numerals rendered thin and small next to Japanese
- **[tabs]** Middle-click could not close a tab once the strip overflowed

## [0.12.2](0.12.2.md) — 2026-08-25

**New / Improved**

- **[agent memory]** Import gained *relocate*, which takes the other environment's history with it, so history, diffs and rollback keep working
- **[resource readout]** Memory, CPU and disk are shown on ECS deployments too

**Fixed**

- **[admin › traffic / Postgres]** The *Traffic* screen would not open, and the MCP traffic statistics failed with it

## [0.12.1](0.12.1.md) — 2026-08-25

**Fixed**

- **[ecs-ec2 / standup]** `--cp-arch arm64` reached the image check but not the stack, so the control plane ran on x86_64 while reporting arm64
- **[cost allocation]** Cost allocation tags are unreadable in an AWS Organizations member account, so per-member cost read "unavailable"
- **[agent memory]** Importing memory into a freshly created workspace failed at the last step

## [0.12.0](0.12.0.md) — 2026-08-25

**CLI pins** — Claude Code 2.1.243 / Codex 0.149.1 / OpenCode 1.18.22

**New / Improved**

- **[idle stop]** A session stopped on a question no longer keeps its workspace awake (it gets its own timeout)
- **[carried-over prompts]** Questions, plans and permission requests open at shutdown are kept and answered from the session list
- **[tool approvals]** *Ask every time* can be chosen per agent kind or per session; the default is unchanged
- **[member handoff]** A session can be offered to another member of the same tenant
- **[idle stop]** A *do not auto-stop* pin holds a session and workspace for four hours
- **[admin › members]** The roster shows when each workspace will stop, and why it will not
- **[presence]** Presence follows recent keystrokes rather than an open connection; sessions with background work are not reaped
- **[handoff]** A handoff can start in a new worktree
- **[models]** New accounts start with Claude Fable hidden

**Fixed**

- **[UI prefs sync]** Accumulated settings such as working sets could leak into another account
- **[workspace start]** Some people hit a red "agent is not responding" on every start
- **[question cards / background work]** Questions raised while a subagent or workflow was running could not be answered
- **[markdown / CJK]** Bold and strikethrough did not work inside Japanese text

## [0.11.0](0.11.0.md) — 2026-08-23

**CLI pins** — Claude Code 2.1.241 / OpenCode 1.18.21 / Antigravity 1.1.19

**New / Improved**

- **[git connections]** Tenant admins register their own git OAuth app; the environment variables are no longer read
- **[distribution / control plane]** The control plane image ships for x86_64 and arm64, selected with `CpArch`
- **[keyboard]** Direct Alt keys for daily actions (tabs/panes, memos, working sets, font size and more), alongside the leader chords
- **[ecs-ec2 › golden]** The admin pool screen shows a golden bake in progress, or why one is not running
- **[reply suggestions]** One-off learned chips can be cleared in bulk
- **[ecs-ec2 ops]** Standing a deployment up, putting it to sleep and tearing it down are scripts

**Fixed**

- **[antigravity]** Sessions could run — and bill — on a different model than the one selected
- **[ecs-ec2 › slot pool]** Free slots did not sleep and kept billing as if in use
- **[machine classes]** Changing a member's machine class could leave their workspace permanently unstartable
- **[workspace power]** A workspace stuck in *starting* could not be stopped from the Console, and gave no reason
- **[control plane restart]** Three things stayed broken until a workspace restart: saving the Bitbucket OAuth connection, all `af_*` tools, and notifications
- **[ecs-ec2 deploy]** A fresh 0.10.0 deployment always rolled the slot pool stack back at the last step
- **[mirror]** Another session's to-do strip stuck to the top and stacked up
- **[mobile / mirror]** Pinch-zooming the mirror pinned the composer to the middle of the screen
- **[ecs-ec2 › golden]** Golden could be baked from a candidate of the other architecture; the admin screen could call a running workspace idle

## [0.10.0](0.10.0.md) — 2026-08-22

**New / Improved**

- **[ecs-ec2 / machine classes]** Tenants and members choose which machine class a workspace runs on, Graviton included
- **[drawio]** Vendor stencils (AWS, Azure, GCP, Kubernetes, BPMN, racks) render
- **[admin › cleanup]** Empty tenants and removed member rows can be deleted
- **[account menu]** The running version and image tag are shown (ECS)
- **[admin]** The reserved `af-golden` tenant is hidden

**Fixed**

- **[stopped sessions / terminal]** After a deployment restart, every stopped session looked like a broken, disconnected terminal
- **[claude state detection]** claude relaunches itself and dropped our conversation id, going silent in the mirror
- **[copilot / cursor]** Went silent the same way
- **[mirror]** Tool traces opened and closed on their own, moving the reading position by thousands of pixels
- **[memos / Postgres]** Memo categories were broken (table never migrated; the error collapsed into an empty list)

## [0.9.3](0.9.3.md) — 2026-08-21

**Fixed**

- **[admin › members]** You could not remove your own membership from any tenant, even with several; only the last one is refused now

## [0.9.2](0.9.2.md) — 2026-08-21

**New / Improved**

- **[ecs-ec2 › golden]** The control plane re-bakes golden when the workspace image changes

**Fixed**

- **[ecs-ec2 › golden]** A home built from golden would not start at all, and the task restarted forever
- **[ecs-ec2]** A workspace whose Start failed before its home volume existed was retried every minute, forever
- **[ecs-ec2 / runbook]** Baking golden by hand could not be completed as written

## [0.9.1](0.9.1.md) — 2026-08-21

**New / Improved**

- **[ECS / git connections]** Bitbucket can be connected on ECS (`BitbucketOauthKey`)
- **[text-to-speech]** VOICEVOX settings are hidden on deployments without the engine

**Fixed**

- **[ECS / workspace reachability]** Workspaces created after the control plane started were unreachable, and every screen reading them returned 502
- **[text-to-speech / ECS]** Speech always failed — the control plane task role lacked `polly:SynthesizeSpeech`
- **[text-to-speech]** The voice shown during playback could name a character that was not speaking
- **[workspace power]** A workspace that could not start said nothing
- **[onboarding]** New users did not get the first-run guide

## [0.9.0](0.9.0.md) — 2026-08-21

**CLI pins** — Claude Code 2.1.238 / Codex 0.149.0 / OpenCode 1.18.19 / Copilot 1.0.80 / Antigravity 1.1.17 / Kiro 2.19.0

**New / Improved**

- **[sign-in]** Sign in with any OpenID Connect provider, or with GitHub
- **[account identity]** One workspace across email changes, other providers and self-added sign-in methods; settings gained an *Account* tab
- **[tenants]** One deployment splits into tenants with their own login URL, roster and sign-in methods
- **[tenants / network restriction]** Tenant admins can restrict where members connect from
- **[ecs-ec2]** A new AWS runtime that keeps home on a disk that survives stopping
- **[workspace size]** Memory, CPU and disk are chosen per member and per tenant
- **[cost]** Actual cloud spend per member, from Cost Explorer
- **[drawio]** `.drawio` opens as a diagram (zoom, pan, theme-aware, bundled viewer)
- **[changed files]** What *this session* touched, in a strip under the mirror header and as a command palette mode
- **[transcript marks]** Marks drawn on a conversation reach the people you shared it with (four colours)
- **[sharing]** Shared conversations carry more: handoff suggestions, reading position, session name and agent icon on the tab
- **[mobile]** Horizontal swipe switches between running sessions; *jump to start of reply*
- **[mirror]** File paths inside replies became links
- **[claude auth]** An expired login is stated with its date, and sending is refused with a reason
- **[rate limits]** A session stopped by a limit says so and when it returns
- **[session-to-session messages]** An intent is now required: *request*, *question*, *answer*, *notice*
- **[onboarding]** New installs start closed (invite only)
- **[ECS / ingress]** WAF can be enabled (rate limiting and IP reputation only, both off by default)
- **[terminal]** Much lighter rendering in Chrome
- **[idle stop]** Idle sessions and workspaces stop themselves (1 h / 2 h by default)
- **[ECS]** The same restart badge as Docker; **[opencode, codex]** transient provider failures are classified as interruptions and take the managed resume path

**Fixed**

- **[terminal / Chrome]** The Console froze for seconds at a time (evicted GPU contexts, then per-character width measurement through layout)
- **[UI prefs sync]** Learned chips, pins, SSM history and key bindings could reset on every device at once
- **[question cards / claude]** Free text typed into a question was discarded when the options had previews
- **[question cards]** Questions and plans awaiting approval were shown twice
- **[usage]** The usage chart stayed one turn behind
- **[markdown / CJK]** Lines like `[pending]: …` vanished from the mirror (parsed as link reference definitions)
- **[start flow]** The first prompt could fail to arrive, or arrive twice
- **[stopped sessions / terminal]** "Cannot reach the Workspace Agent" while the agent was answering fine
- **[schedules]** Scheduled runs with reporting off had no badge in the mirror
- **[mobile]** Horizontal swipe was completely dead in particular sessions (one unbreakable long string in the transcript)
- **[opencode / plans]** Sessions started on a plan nobody had selected
- **[ECS / guide]** The member guide would not open; **[settings › toolchains]** a JDK can be installed with one button
- **[ECS]** OAuth inside a just-woken workspace, gateway timeouts on first start, a restart badge that lit with nothing changed

## [0.8.0](0.8.0.md) — 2026-08-13

**CLI pins** — Claude Code 2.1.231 / OpenCode 1.18.18 / Copilot 1.0.79 / Antigravity 1.1.12 / Cursor 2026.08.11 / Kiro 2.18.0 / rtk 0.45.0

**New / Improved**

- **[sharing]** Share a conversation with other members of your tenant (view only, or allowed to suggest)
- **[session-to-session messages]** One short message to another session in the same workspace (off by default)
- **[fork]** Fork a conversation from one of your earlier messages (claude / codex / opencode / Copilot)
- **[layout]** The main area can be a grid of tabs, not only splits
- **[agent instructions]** Your own standing instructions for every agent
- **[MCP / project]** See the MCP servers a repository has committed, copy them across agents, or git-ignore them
- **[browser handoff]** Touch support; retarget to another tab in the handed-over page, and results reach the session
- **[codex]** Screenshots appear in the mirror
- **[opencode]** Sakana AI added as a provider preset
- **[cleanup]** Archive a repository's stopped sessions in bulk
- **[markdown]** Code blocks wrap by default
- **[sharing performance]** Shared conversations render far faster; **[assistant]** the assistant can be turned off for good
- **[control plane]** Multiple replicas tolerate each other; **[MCP]** the built-in server takes a fresh name each start

**Fixed**

- **[browser handoff]** Clicks, keys and scrolling in a handed-over page were all discarded (the attachment starts view-only)
- **[terminal / tabs]** Switching tabs kept showing the previous tab's terminal
- **[codex / question cards]** Multi-question prompts were answered against the wrong question
- **[composer]** Pressing send twice quickly froze the input
- **[session state]** A session running a slash command was judged idle
- **[handoff]** Only the last of several handoff suggestions survived
- **[sharing]** The shared list showed only old archives
- **[assistant / opencode]** The assistant fell back to the free tier with nothing connected
- **[plans / question cards]** The state badge read "awaiting permission"; plan comments that were never delivered showed as sent
- **[lightweight preview]** Downstream requests could not resolve the tenant, breaking pages that fetch anything
- **[display]** Dark-theme quotes, pane buttons on the wrong row, wrapped colour swatches
- **[pane pop-out]** Leaked WebGL contexts; **[codex]** thread config merged instead of replaced, and more

## [0.7.0](0.7.0.md) — 2026-08-07

**CLI pins** — Claude Code 2.1.224 / Codex 0.147.0 / OpenCode 1.18.15 / Copilot 1.0.78 / Antigravity 1.1.11 / Cursor 2026.08.04 / Kiro 2.16.2

**New / Improved**

- **[opencode auth]** Sign in from the Console (device flow; a running daemon picks the credentials up without a restart)
- **[opencode plans]** Choose the plan a workspace uses — free, Go or Zen; free tier gets no key injected
- **[MCP]** The AWS Agent Toolkit joins the built-in integrations (read-only by default)
- **[turn auto-resume]** An interrupted claude turn resumes itself (up to twice, on by default)
- **[handoff suggestions]** An agent can propose the next session with a ready first prompt and name
- **[start flow]** A session can start in a folder below the working copy
- **[worktree]** Parent commits can be fast-forwarded into a worktree; diverged history is refused
- **[claude models]** Keep your own model list
- **[reply suggestions]** `Tab` cycles through the chips
- **[appearance / markdown]** Five more colours; copy on quote blocks; a wrap toggle on code blocks
- **[claude auth]** An expired login reads as a failure and points at re-authentication
- **[rate limits]** Visible where they stopped you (managed codex, and model-specific claude limits)
- **[question cards]** Answering a choice is two steps: choose, then confirm

**Fixed**

- **[transcript]** Turns could disappear from the mirror forever (the read cursor skipped a torn line)
- **[schedules]** A schedule could keep firing forever where nobody could see it
- **[worktree / submodules]** A worktree with unfetched submodules was wedged; it now says so and repairs itself
- **[markdown / CJK]** Tables written with the full-width `｜` did not render as tables
- **[reply suggestions]** Suggestions were built from progress fragments rather than actual answers
- **[opencode]** Model list, disconnecting, key changes reaching the daemon, final answers arriving in the mirror
- **[mirror]** Landing position on open, reply footer showing the end time, `"` truncating the view
- **[start flow]** The phone's soft keyboard opened on its own; a failed connection check left the start button stuck

## [0.6.0](0.6.0.md) — 2026-08-02

**CLI pins** — OpenCode 1.18.11 / rtk 0.44.2

**New / Improved**

- **[browser handoff]** Hand the page an agent is driving to a person; the agent never makes the final publish/send/consent click
- **[display settings]** Thinking expansion is configured per agent kind (off by default)
- **[i18n]** The Console speaks the display language (assistant, suggestions, compact carry-over, work plans)
- **[completion reports]** Readable in either language
- **[distribution / compose]** ⚠️ Breaking: images come from GHCR instead of a tar

**Fixed**

- **[usage chip / claude]** The chip could freeze for a whole day (the status line was re-wrapped until it could no longer run)

## [0.5.1](0.5.1.md) — 2026-08-01

**New / Improved**

- **[release gate]** Every artifact is unpacked recursively and scanned before publishing
- **[commit graph]** Branches and tags collapse into one icon instead of a row of labels

**Fixed**

- **[artifacts]** The Console bundle carried source comments (0.1.0–0.5.0 downloads withdrawn)
- **[i18n]** With reply language *auto*, an English Console still got Japanese answers

## [0.5.0](0.5.0.md) — 2026-08-01

**CLI pins** — Codex 0.146.0 / OpenCode 1.18.10 / Copilot 1.0.77 / Antigravity 1.1.9 / Kiro 2.16.0 / rtk 0.44.1

**New / Improved**

- **[working sets]** Group repositories, chats and sessions per project and switch the left pane between them
- **[MCP]** Register your own MCP servers (stdio or remote HTTP, with a connection test; tenant admins can distribute them)
- **[agent memory]** Version the memory files: history, diffs, rollback, export and import as a bundle
- **[editor]** Ask for an edit to the open file and accept it from a diff
- **[skill picker]** Pick skills, with arguments, from the session chat
- **[plans]** Annotate a passage of a plan and send the comments together
- **[rate limits / claude]** Automatic recovery: confirm the free "wait for reset" and schedule a single resume
- **[agent switch]** Switch agent mid-conversation; **[models]** hide models you never use
- **[plan carry-forward]** Work plans survive a handoff instead of being compacted away
- **[token reduction]** Much less spend between assistant and sessions (five knobs in settings)
- **[light mode]** Rebuilt on measured contrast
- **[cleanup]** Staged: archive → remove worktree → release the shelf
- **[worktree]** Create one on an existing branch; **[restart badge]** the workspace bar flags a stale backend
- **[left pane, markdown, naming]** Files grouped by project and branch, YAML front matter as properties, auto-linked hashes and ids, clearer names

**Fixed**

- **[rate limits / session state]** A session could stay stuck in *running* after the limit menu, with nothing but a restart to fix it
- **[MCP / opencode]** Every MCP tool disappeared on OpenCode 1.18.8
- **[completion reports]** Reports could be lost (opencode assistant sessions never delivered any; codex sometimes failed)
- **[file search / skill picker]** The Console could go completely black
- **[restart badge / Docker]** The badge would not clear; **[AI assist]** generated subjects came with a preamble and ignored the display language

## [0.4.0](0.4.0.md) — 2026-07-27

**New / Improved**

- **[usage]** A usage tab per feature, with sessions and the fleet's own assist calls in one ledger
- **[editor]** Text and code editing in the File pane (unsaved buffer with undo, revision check)
- **[deletion locks]** Pin sessions, working copies and conversations; enforced at the REST layer
- **[turn auto-resume]** Turns that failed for a resendable reason are resumed (twice at most, on by default)
- **[assistant / models]** Separate models for chat and for assist generation
- **[opencode]** Model list filtering and visible failed turns; **[mobile]** settings open from a list

**Fixed**

- **[assistant chat]** Answers could cross between conversations when another chat was opened
- **[deletion locks]** The lock state was not reflected until reload
- **[plans]** The approval badge was wrong on a re-presented plan
- **[file save]** Save/restore races: an abandoned PUT still landing, a stale read looking like a successful save
- **[usage ledger]** Missed rows, blocked writes, double counting

## [0.3.0](0.3.0.md) — 2026-07-25

**CLI pins** — Claude Code 2.1.220 / Codex 0.145.0 / OpenCode 1.18.5 / Copilot 1.0.75 / Antigravity 1.1.7 / Cursor 2026.07.23 / Kiro 2.14.2 (new)

**New / Improved**

- **[agent kinds]** Kiro CLI support (the seventh)
- **[reply suggestions]** Learned replies as chips, plus ✨ to generate one in context
- **[fleet operator]** The operator can answer a session's questions and approve or reject plans, including an unattended autopilot
- **[schedules]** A schedule can fire into the operator conversation instead of a session
- **[memos]** Collapsible categories
- **[native / updates]** The host-update notice moved to the top bar
- **[CLI self-update]** Self-update actually moves forward now (one shadow scheme for every tool)
- **[usage]** Opus 5 / Sonnet 5 counted as 1M context; **[mobile]** agent selection by horizontal swipe
- **[distribution / licensing]** `LICENSE` and `NOTICE` in the dist repo, and release notes from here on

**Fixed**

- **[completion reports / background work]** A report could be lost when the session ended while subagents were running
- **[transcript]** A skill emitting command tags in reverse order could erase the user's whole turn from the mirror
- **[schedules / cleanup]** Deleting a worktree or session an active schedule points at is refused instead of silently breaking it

## [0.2.3](0.2.3.md) — 2026-07-24

**New / Improved**

- **[memos]** Drag a memo into a session's composer
- **[git connections]** The settings explain the scopes a GitHub token needs
- **[left pane]** Reloads after a clone or checkout, so new working copies appear
- **[native / svn]** The account menu is always visible; SVN folder naming and wording fixed

**Fixed**

- **[pane pop-out]** The pop-out button overlapped the pane header's controls

## [0.2.2](0.2.2.md) — 2026-07-24

**New / Improved**

- **[pane pop-out]** Any pane can be detached into its own browser tab
- **[schedules]** A detail and edit modal
- **[assistant chat]** Cursor can drive it
- **[memos / assistant]** A `⋯` menu on the rows, with delete moved inside it
- **[settings modal]** Bigger, with the current tab as the heading on phones; **[session limits]** shell / SSM sessions no longer count
- **[handoff / command palette]** Options aligned with the start dialog; arrow selection scrolls into view

**Fixed**

- **[cursor / assistant]** A headless Cursor could perform write operations without confirmation
- **[schedules]** Scheduled runs reusing a long-lived session could vanish silently

## [0.2.1](0.2.1.md) — 2026-07-23

**New / Improved**

- **[handoff]** Carrying work into a new session is one modal (formerly *fork*)
- **[git connections / Bitbucket]** The connection card names the scopes and checks them on connect
- **[cursor]** A model badge per reply; **[key bindings]** the send key moved to the *Keys* tab

**Fixed**

- **[git connections / Bitbucket]** Clone and push with an API token failed to authenticate
- **[mirror]** Answers, plan approvals and delegations did not appear while a session was running

## [0.2.0](0.2.0.md) — 2026-07-23

**CLI pins** — Cursor 2026.07.20 (new)

**New / Improved**

- **[chat bridge]** Drive sessions from Discord and Slack (a thread per session, buttons for questions, plans and permissions)
- **[schedules]** Cron, interval and one-off runs; a stopped workspace is woken and returned to its previous state
- **[agent kinds]** Cursor CLI support (the sixth)
- **[svn]** Subversion checkouts (basic auth, sub-paths, self-signed certificates)
- **[native / auto-update]** The `af` host binary updates itself; applying it stays an explicit action
- **[copilot]** Model selection and the usage chip
- **[console↔CP traffic]** Much less of it: gzip, ETag revalidation, four polls replaced by one SSE stream
- **[settings modal]** Ten flat tabs became three groups; **[guide]** the user guide is bilingual
- **[mirror]** Honest scroll following, a permanent *jump to latest*, no blank before replies, lightbox responds to Back

**Fixed**

- **[plans]** Rejecting a plan was recorded as approval
- **[remote control]** The default is off (applied once to existing workspaces)

## [0.1.2](0.1.2.md) — 2026-07-21

**New / Improved**

- **[workspace start]** A progress dialog showing which stage boot-install is in
- **[distribution / README]** Getting started, feature list and uninstall, with a Japanese translation

**Fixed**

- **[claude / native]** Claude sessions accepted no input at all (a `git credential` prompt stole the pty)
- **[workspace start]** A failed download during first start no longer leaves a half-built workspace (it retries)
- **[native / `af start`]** The first `boot-install` streams its progress to the terminal

## [0.1.1](0.1.1.md) — 2026-07-21

**New / Improved**

- **[settings › assistant]** Assistant settings split into their own tab
- **[artifacts / i18n]** Everything user-facing in the artifacts is English; the README opens differently

**Fixed** — none

## [0.1.0](0.1.0.md) — 2026-07-21 (first public release)

**CLI pins** — Claude Code 2.1.215 / Codex 0.144.6 / OpenCode 1.18.3 / Copilot 1.0.73 / Antigravity 1.1.5 / rtk 0.43.0

**New**

- **[agent kinds]** Five agent CLIs from one Console (Claude Code, Codex, OpenCode, Copilot, Antigravity)
- **[workspace]** Per-user isolation: CPU and memory quotas, persistent home, network separation
- **[repos / worktrees]** Parallel sessions on real git repositories (HTTPS clone, LFS, submodules)
- **[control plane]** Orchestration, encrypted credentials, quotas, admin screens
- **[distribution]** Two forms: a Docker-free native package and a Docker Compose bundle
