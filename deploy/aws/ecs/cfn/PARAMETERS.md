# `30-ingress.yaml` — parameter reference

The long-form reasoning for every parameter of the ingress / Control-Plane stack. The
template itself keeps a one- or two-line `Description:` per parameter — enough to know what
to pass — and this file holds the rest: the measurements, the trade-offs and the traps.

**Why the split.** CloudFormation refuses a template body over **51,200 bytes** and
`30-ingress.yaml` crossed that line, so `env.sh` has to hand it over through S3
(see [the 51,200-byte wall](../README.md#the-51200-byte-wall-why-a-deploy-can-stop-working-without-anyone-touching-it)).
The prose was 40% of the file — 21,630 bytes of the 54,681 — and prose is the one part that
does not have to travel to the API to do its job. YAML comments count toward the limit
exactly like `Description:` does, so moving a paragraph into a `#` comment would have saved
nothing; it had to leave the file. Nothing here was shortened in the move.

Order matches the template. Parameters not listed here are self-explanatory in it
(`NetworkStackName`, `PlatformStackName`, `CpPort`, `CpCpu`, `CpMemory`, …).

- [Images and workspace defaults](#images-and-workspace-defaults)
- [EC2 slot pool (`WsRuntime=ecs-ec2`)](#ec2-slot-pool-wsruntimeecs-ec2)
- [Scheduled execution](#scheduled-execution)
- [WAF](#waf)
- [Public host and preview subdomains](#public-host-and-preview-subdomains)
- [Control Plane task](#control-plane-task)
- [Sign-in](#sign-in)
- [Alarms](#alarms)

## Images and workspace defaults

### `ImageTag`

Tag of BOTH the af-control-plane and af-workspace images in ECR (=VERSION;
`release-ecr.sh` pushes the pair under one version). Upgrade = push a new tag, then
re-deploy with only this parameter overridden: the CP service rolls to the new image;
workspaces pick it up on their next Start (running ones are untouched — the adapter builds
task definitions statelessly).

### `WsTaskCpu`

Default Fargate CPU units for a workspace task (1024 = 1 vCPU). Valid values are
256/512/1024/2048/4096/8192/16384 — Fargate accepts only specific (cpu, memory) pairs
(docs/log/63 §63.2). Per-user overrides are set in the Console and are snapped onto a valid
pair, so this is only the floor everyone starts from.

### `WsTaskMemory`

Default workspace task memory in MiB. Must be valid for `WsTaskCpu`: 1 vCPU allows
2048-8192 in 1024 steps, 2 vCPU 4096-16384, 4 vCPU 8192-30720; 8 vCPU steps by 4096 and
16 vCPU by 8192. A development workspace measured 1.12 GiB anonymous and 5.30 GiB peak
including page cache, so 2048 is tight for real use.

### `WsDiskGiB`

Default per-workspace WORKING disk in GiB. 0 keeps Fargate's free 20 GiB; 21-200 sets the
task's ephemeral storage (billed only above 20 GiB). This disk is wiped when the workspace
stops — only the EFS home persists.

It is also the switch for moving the small-file caches (go build cache, uv, go modules) and
build artifacts (node_modules, target, .venv) off EFS onto the task-local disk: the
entrypoint does that only when this is 30 or more, because the measured caches do not fit
alongside the image in 20 GiB (ADR 0044 決定 3). The default is 50 so that relocation is
ON — at 0 it can never arm, which is what shipped before and meant every deployment ran
builds on EFS at ~9x the cost. Roughly $2.9/month per workspace at 24/7, and only while the
task runs. Set 0 to opt out.

### `WsRuntime`

Which Runtime adapter the Control Plane uses. `ecs` = Fargate (the shipped default: two
resources per workspace, ~105s to start, home on EFS). `ecs-ec2` = a pool of EC2 slots with
a persistent per-user EBS home (docs/log/64 §64.12/§64.17, ADR 0045 決定 10/12).

Switch for I/O and persistence (small-file writes 8-30x faster than EFS, a home that
survives) and for sizes above Fargate's ceilings — NOT for start latency: measured 43-110s
against Fargate's ~105s. The price is six resource kinds per workspace on a substrate with
no production mileage. Requires the 40-ec2-pool stack and `Ec2SlotLaunchTemplate`. Falling
back is this one value.

## EC2 slot pool (`WsRuntime=ecs-ec2`)

### `Ec2SlotLaunchTemplate`

`SlotLaunchTemplateId` output of the 40-ec2-pool stack (lt-…). Required when
`WsRuntime=ecs-ec2` — the CP refuses to boot on that profile without it, rather than
silently running no workspaces.

### `Ec2SlotTypes`

The slot sizes the pool may run. A workspace lands on the smallest slot that holds its
memory REQUEST and gets the WHOLE box (the task reserves neither cpu nor memory — on EC2
those are reservations against the instance). EC2 on-demand pricing is perfectly linear in
vCPU, so bigger slots are not more expensive per user (ADR 0045 決定 8). The vCPU field is
optional and display-only: the Console prints it so a tenant admin can see which box a
memory number lands on, and omitting it just omits the label (ADR 0045 決定 21 — asking EC2
instead would mean an extra IAM action for a label).

Two shapes, and the old one is not deprecated (docs/log/70 §70.4.2):

```
instanceType:memoryMiB[:vcpu],…                one unnamed class, x86_64
id|label|arch|<ladder>[;id|label|arch|<ladder>] one class per entry
```

With more than one class, a tenant admin chooses WHICH KIND of machine per tenant and per
member, and the memory number still picks the rung within it. `arch` is `x86_64` or `arm64`
and is DECLARED, never guessed from the family name.

The template's default offers two x86_64 classes, because m6i needs NOTHING to work: same
architecture, so the same workspace image, the same AMI, the same golden snapshot, and a
member's home stays valid when they move (measured ap-northeast-1 2026-08-22: m6i is 4.8%
cheaper than m7i). It is the "cost over speed" choice that is available today.

⚠️ Changing this parameter moves nobody by itself — `Ec2DefaultSlotClass` decides where
members with no choice of their own land, and it defaults to the FIRST class. An existing
stack also keeps its own value on a redeploy unless you pass this parameter, so upgrading
does not add a picker to a running deployment.

Adding Graviton is a matter of appending a class. Measured in ap-northeast-1 on 2026-08-22
(price, and this repository's own build times on each family):

| Family | Price / hour | Build time | Verdict |
|---|---|---|---|
| m8g | -11.0% | 29% **faster** than m7i | strictly better than m7i |
| m7g | -19.0% | 10% faster | no reason to pick over m8g |
| m6g | -24.0% | 32% **slower** | cheapest per HOUR, not per unit of work |

```
…;arm|Faster and cheaper (Arm)|arm64|m8g.large:8192:2,m8g.xlarge:16384:4,m8g.2xlarge:32768:8
```

⚠️ m6g is the cheapest per hour and the most expensive per build. Which one is right depends
on the member: a slot bills for the wall-clock time it is RUNNING, and a workspace spends
most of that idle, so the hourly price is what a mostly-reading member pays — while a member
who builds all day pays in time. If you offer m6g, put the trade-off in its LABEL; "24%
cheaper" on its own will be read as free money.

⚠️ Unlike m6i, an arm64 class has TWO prerequisites, in this order:

1. a workspace image with an arm64 manifest. Without it the slot launches and the task
   cannot pull anything that runs on it.
2. `Ec2SlotAmiArm64`. The CP refuses to boot when an arm64 class is declared and that is
   empty, rather than showing a class in the admin UI that cannot launch.

⚠️ Adding a class moves nobody: `Ec2DefaultSlotClass` still decides where a member with no
choice of their own lands. Changing the class of a member who already has a home makes their
next start reinstall its architecture-dependent tools (docs/log/70 §70.5) — that is
expected, and it costs them one slow start.

### `Ec2SlotAmiArm64`

`SlotAmiIdArm64` output of the 40-ec2-pool stack. Required only when `Ec2SlotTypes` declares
an arm64 class — the CP refuses to boot otherwise, rather than showing a class in the admin
UI that cannot launch.

### `Ec2DefaultSlotClass`

The class id a member lands on with no per-user and no per-tenant value. Empty = the first
class declared in `Ec2SlotTypes`.

⚠️ Moving this on a running deployment moves EVERY member who has not chosen for themselves,
and each of their homes then reinstalls its architecture-dependent tools on the next start.

### `Ec2MaxSlots`

Hard cap on instances in the pool. A slot is one concurrently RUNNING workspace, so size
this like "how many people work at once", not "how many accounts exist". Start fails with a
clear error at the cap instead of growing the bill.

### `Ec2HostReserveMb`

How much of a slot is held back from the WORKSPACE so the box's own daemons (dockerd,
containerd, the ECS agent, SSM, the EFS stunnel) cannot be starved by it.

- `auto` = a fifth of the rung, clamped to 1-2 GiB: an 8 GiB slot's workspace is capped at
  6.4 GiB, and the Console prints that number with the box beside it.
- `off` = uncapped, which is what every deployment did before this existed — and what melted
  a live slot on 2026-08-27 (docs/log/64 §64.40): one workspace took enough anonymous memory
  that the kernel evicted all page cache, every daemon spent hours re-reading its own
  executable off disk, and the box stopped answering the cluster while still looking healthy
  to EC2.
- A plain number is that many MiB.

NOTE: this is part of the task definition, so changing it replaces the running task at the
owner's next start, exactly as an image change does.

### `Ec2SlotSleepSec`

How long a slot may sit with no running task before the instance is STOPPED (not terminated
— the image cache lives on its root volume).

NOT the same thing as the idle-stop the product already has: `AF_WS_IDLE_TIMEOUT` / the
per-tenant `ws_idle_timeout` watches the PERSON and stops their WORKSPACE on every runtime,
while this one starts counting after that has happened and puts the BOX to sleep. They run
in series (person leaves → workspace stops → slot sleeps). A sleeping slot costs only its
root volume (~$9.6/month at 100 GiB) instead of ~$95 running, and its owner wakes it in
~110s.

It governs BOTH kinds of dormant slot, from this one value: a slot still holding its owner's
home (which stays ATTACHED while the slot sleeps — that is what keeps "the same user gets
the same slot"), and a slot holding NO home at all, freed by an eviction, a size/class
change, a Destroy, or the golden bake. No warm spare is kept: an empty RUNNING slot saves
the next arrival ~67s and costs a full instance-hour, and this deployment keeps the box
STOPPED instead (docs/log/64 §64.31).

Set 0 to keep every slot running forever (fastest returns, highest bill).

### `Ec2SlotTerminateAfterSec`

The step AFTER `Ec2SlotSleepSec` on the same clock, and the only one that gives the ROOT
VOLUME back: past this the box is TERMINATED rather than left stopped.

**Why you want it.** Nothing else ever removes a box, so the number of retained roots only
grows and its ceiling is `Ec2MaxSlots`. Raising `Ec2MaxSlots` to serve more people therefore
also signs you up for `Ec2MaxSlots` × `SlotRootVolumeGiB` of gp3, permanently and whether or
not anybody is working — 30 × 40 GiB × $0.096/GB-month is ~$115 a month of stopped boxes.
Measured on a live deployment; the only cure was terminating them by hand (docs/log/64
§64.32).

**What it costs your users.** 25 seconds, once, and only for the first arrival after the box
is gone. Waking a dormant box that still holds its owner's home is 110s; building a new one
from scratch is 135s. The image cache on the root volume saves the 32s cold pull, and
instance boot plus ECS re-registration spends it again.

There is deliberately NO warm floor ("keep N stopped boxes"). A dormant box is only reusable
BY ITS OWNER — for anybody else the wake, the attach and the mount SSM round trip come to
123-143s against 135s for a fresh box — so a shared warm box is worth about nothing, and a
floor would hold specific people's boxes through weekends and shutdowns. The 92 seconds that
IS worth buying belongs to a RUNNING free slot, which costs ~$95/month, not $3.84.

0 (the default) = never terminate, which is what every deployment did before this existed.
14400 (4h) is the recommended value: come back the same day and you get the 110s path, come
back tomorrow and you pay 135s. Must be ≥ `Ec2SlotSleepSec` to mean "sleep, then terminate";
a smaller value simply skips the sleeping stage.

### `Ec2HibernateAfterSec`

The DEPLOYMENT DEFAULT for how long a home may sit unopened before it is snapshotted and its
volume deleted; the next start restores it (ADR 0045 決定 4 and 13-2). A tenant overrides it
from the Console (Tenants → the tenant → "Hibernate unused homes"), so this is only what a
tenant that has not chosen gets.

⚠️ This is the only automatic path in the product that moves a user's HOME, which is why 0
(off) is the default and why it is reversible: hibernation snapshots first and deletes only
once the snapshot completes, so nothing is destroyed. What the user pays is the restore on
their next start (about 122s) and a slower first day (restored blocks hydrate from S3
lazily, ~2.3x on first touch).

What it saves: a home is billed on PROVISIONED size ($0.096/GB-month) and a snapshot on USED
blocks ($0.05/GB-month), so a 50 GiB volume holding 20 GiB goes from $4.80 to about $1.00 a
month. Judge it by last-opened date, not by size.

Until this parameter existed the value could not be set on an ECS deployment at all — there
was no CFN parameter and a hand-edited task definition is overwritten by the next deploy.
2160h (90 days) is a reasonable starting point; leave it 0 and set it per tenant if the
retention is theirs to choose.

### `Ec2HomeGiB`

Default size of a user's persistent home volume (gp3). Unlike EFS this is billed as
PROVISIONED ($0.096/GB-month vs EFS $0.36 for what you use), so the break-even fill rate is
26.7% — do not hand out volumes far larger than people fill.

## Scheduled execution

(docs/log/38)

⚠️ EMPTY means "keep the product default" for all four. The defaults live in the CP
(`control-plane/main.go`); copying them into the template would freeze this deployment on
whatever they were the day the stack was written. Until these existed the values could not be
set on an ECS deployment at all (a hand-edited task definition is overwritten by the next
deploy) — the same gap `Ec2HibernateAfterSec` closed.

### `SchedulerInterval`

Tick cadence of the scheduled-execution loop (Go duration, default 1m). `"0"` hard-disables
timed execution: nothing is ever woken by the clock, the Console hides the Schedules section
and the operator warns that a schedule will never fire. Set it only if unattended wakes are
unwanted for cost or policy reasons; the tick is one indexed due-query and is a no-op while
no schedule exists.

### `ScheduleWakeTimeout`

How long one fire waits for the woken workspace's Agent to answer (Go duration, default
300s = the window the platform already grants a boot to call itself "starting").

⚠️ Do not lower this on ecs-ec2 without measuring. The clock starts at the WAKE, ~20s before
the adapter's own "Agent healthy Ns after Start" clock (the slot's `StartInstances`, ECS
registration and mount run in the background convergence), so the budget is tighter than
that log line reads. Measured: the SAME workspace took 65-131s on consecutive mornings, so a
budget inside that spread makes a stopped workspace's morning fire land or drop at random
(docs/log/38, docs/log/64). A miss is retried for 15 minutes, but each retry re-wakes the
workspace.

### `ScheduleSettle`

How long a workspace woken by a schedule is held up afterwards so the reaper cannot stop it
out from under the session it just started (Go duration, default 5m). Also covers auto-turn
follow-ups and a user opening the Console right after.

### `ScheduleJitter`

Maximum deterministic spread applied to CRON fires so that everyone whose schedule says
09:00 does not wake at once (Go duration, default 2m; `"0"` = fire on the exact slot).
Derived from the schedule id, so it is stable across restarts and `next_run`, the read-back
confirmation and `{{time}}` keep showing the requested time. On ecs-ec2 every wake is also a
slot, so keep some spread if many people share one morning hour.

## WAF

Optional, OFF by default.

⚠️ ONLY rate limiting and IP reputation. The signature rule sets (Core rule set, SQLi, XSS,
LFI) are deliberately NOT offered here: this product carries SOURCE CODE AND SHELL COMMANDS
in ordinary request bodies — chat messages, file writes, terminal input — so strings like
`'; DROP TABLE`, `../../etc/passwd` and `<script>` are legitimate traffic. Those rules would
403 real work at random, and it would look like a product bug rather than a WAF decision
(the expensive part is the hours spent before anyone suspects the WAF). WAF also inspects
only the first 8 KB of a body by default, so a signature set here buys less than it appears
to.

The cheaper, stronger control for a private deployment is upstream of this: 00-network's
`AlbIngressCidr`, which stops the traffic at the security group.

### `WafRateLimitPer5Min`

Requests per 5 minutes from one IP before WAF blocks it (0 = no WAF at all). Counts every
request including Console polling, so keep it well above what a whole office behind one NAT
produces — 3000 is a sane starting point.

### `WafIpReputation`

Add AWS's managed IP reputation list (known scanners/botnets). Matches on the SOURCE, not on
the body, so it cannot mangle legitimate agent traffic. Requires `WafRateLimitPer5Min` > 0
(that is what creates the web ACL).

## Public host and preview subdomains

### `Fqdn` / `HostedZoneId`

`Fqdn` is the public host for the Console/API (ACM cert + Route53 alias), e.g.
`af.example.com`. `HostedZoneId` is the Route53 hosted zone id for its domain; the zone must
be in this account (ACM DNS validation and the alias record are created in it). Both are
required — pass your own domain.

### `PreviewDomain`

Parent domain for the per-start preview subdomains (docs/81), e.g. `pv.example.com`. A
workspace start mints a random slug and its services become reachable at
`https://{slug}-{port}.{PreviewDomain}` — 3000 と 8080 のような 2 ポート構成が、サブパスでは
なく **それぞれのルート直下**で開ける。

Empty (the default) = the feature is OFF and nothing for it is created; only the existing
path-mode `/preview/{port}` exists.

★ ラベルは 1 段しか使えない。ACM のワイルドカード証明書が 1 段しか受け持たないので、ポートは
`{slug}-{port}` とラベルの中に前置してある（`{port}.{slug}.…` は `*.*.…` を要求して発行できない
— ADR 0062 決定 2）。

⚠️ Console の FQDN の **子ではなく兄弟**を勧める（`af.example.com` に対して `pv.example.com`）。
子（`*.af.example.com`）にすると、プレビューで動くアプリが `.af.example.com` のドメイン cookie を
書けてしまい、Console の cookie を上書き / 固定できる余地が残る（ADR 0062 決定 13）。

⚠️ この名前を含むゾーンが `HostedZoneId`（または `PreviewHostedZoneId`）でなければ、ACM の DNS
検証が終わらず **失敗ではなく「進まない」**形でスタックが止まる。

### `PreviewHostedZoneId`

Route53 hosted zone id that contains `PreviewDomain`, when that is NOT the zone the Console
lives in. Empty (the default) = use `HostedZoneId`, which is right whenever `PreviewDomain`
sits inside the Console's own zone.

★ 兄弟を勧めている（上記・ADR 0062 決定 13）のに Console のゾーンが委任されたサブドメイン
（`af.example.com` そのものが 1 つのゾーン）だと、`pv.example.com` は **そのゾーンの外**になる
—— 兄弟にするには別ゾーン＋親からの NS 委任が要り、その id をここで渡す。これが無いと「勧めて
いる形が、このテンプレートでは表現できない」ことになる。

⚠️ Console 側（`Cert` / `DnsRecord`）は常に `HostedZoneId` のままで、ここが効くのはプレビューの
証明書検証とワイルドカード A レコードだけ。

## Control Plane task

### `CpArch`

Which CPU architecture the Control Plane's own Fargate task runs on (docs/log/72). `arm64` =
AWS Graviton: same task sizes, ~20% cheaper per vCPU-hour, and Service Connect works on it
(AWS added Fargate/Graviton support to Service Connect in December 2022). Fargate platform
version 1.4.0 or later is required, which LATEST already is.

⚠️ The prerequisite is the IMAGE, and there is exactly one: the tag in `ImageTag` must carry
an arm64 manifest. Releases publish one only when publish-dist is run with
`control_plane_arm64` — it is off by default, so an ordinary release is amd64-only and
setting this to arm64 against it leaves the service unable to place its task at all. Check
before switching:

```sh
crane manifest <repo>/af-control-plane:<tag> | jq -r '.manifests[].platform'
# or: docker buildx imagetools inspect <repo>/af-control-plane:<tag>
```

⚠️ `ImageTag` is shared by the CP and the workspace image, and their architectures are
decided separately — this parameter for the CP, `Ec2SlotTypes` for the slots. A deployment
can perfectly well run an arm64 CP over x86_64 slots or the reverse; neither reads the
other's architecture.

This defaults to `x86_64` rather than to "unset" on purpose. Fargate assigns X86_64 when
`runtimePlatform` is omitted, so the default changes nothing about a running deployment —
but it is now WRITTEN DOWN. Left implicit, the day the image gains an arm64 manifest is the
day the architecture becomes a property of whatever Fargate happened to pick, and nobody
would see it in the template.

### `SsmPrefix`

SSM SecureString path prefix holding the CP secrets, created out of band before deploy (see
[the README](../README.md#prerequisites-once-per-account)). Params: `cookie-secret`,
`master-key`, plus `google-client-secret` / `oidc-client-secret` / `github-client-secret`
depending on the IdPs in use.

## Sign-in

### `GoogleClientId`

Google OAuth client id (not secret; the secret comes from SSM). Leave empty to sign in with
the OIDC provider instead.

### `OidcProviderId`

Enable an OIDC IdP (Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab) under this id —
UPPERCASE, e.g. `ENTRA` (it names the `AF_OIDC_<ID>_*` env). Empty = Google only. Its client
secret must exist at `<SsmPrefix>/oidc-client-secret`. docs/log/61.

### `OidcIssuer`

`AF_OIDC_<ID>_ISSUER`, e.g. `https://login.microsoftonline.com/<tenant-guid>/v2.0`.

★ Pin Entra to your tenant guid — with `/common/` or `/organizations/` every Microsoft
account in the world reaches the login and personal accounts can rewrite their own email, so
the CP refuses to start unless `OidcAllowedTids` is set.

### `OidcClientId` / `OidcLabelJa` / `OidcLabelEn`

`AF_OIDC_<ID>_CLIENT_ID` (not secret; the secret comes from SSM), and the login button
labels in Japanese / English (default is generated from the id).

### `OidcTrust`

`AF_OIDC_<ID>_TRUST` — why this IdP's email may be believed. `issuer` = the issuer is pinned
to one tenant (Entra ID, which never emits `email_verified`). `email_verified` = accept only
when the IdP asserts that claim (Okta / Keycloak / Auth0).

### `OidcAllowedTids`

`AF_OIDC_<ID>_ALLOWED_TIDS` — accepted Entra tenant ids. Required only when the issuer is
the `/common/` or `/organizations/` endpoint.

### `GithubAllowedOrgs`

`AF_GITHUB_ALLOWED_ORGS` — comma-separated GitHub orgs whose ACTIVE members may sign in.
Setting this is what enables the GitHub button; its OAuth App secret must exist at
`<SsmPrefix>/github-client-secret`, and the app needs the redirect URI
`https://<Fqdn>/oauth2/callback`.

★ If the org restricts third-party OAuth apps, an org owner must approve the app or everyone
is rejected. docs/log/61 §61.7.

### `GithubClientId`

`GITHUB_OAUTH_CLIENT_ID` (not secret; the secret comes from SSM). Sign-in only since
docs/log/71 — the Console's GitHub "Connect" button reads the OAuth app the TENANT
registered (Tenant settings → Integrations), not this one.

### `GithubAllowedDomains`

`AF_GITHUB_ALLOWED_DOMAINS` — strongly recommended. GitHub hands us the account's PRIMARY
VERIFIED address, which is a personal one for most people, and a personal address is a
different person here: they get a new empty workspace rather than their own. Empty = the
deployment-wide allowlist.

### `AllowedEmails` / `AllowedDomains`

`AF_OAUTH_ALLOWED_EMAILS` and `AF_OAUTH_ALLOWED_DOMAINS` (comma-separated addresses / email
domains) permitted to sign in. Somebody invited to a tenant in the Admin panel also gets in
without being listed here, so an invite-run deployment can leave both empty. Both empty AND
nobody invited ⇒ every login is denied (fail closed), which is what a fresh stack looks
like — set one to get your first admin in.

### `SuperAdminEmails`

`SUPER_ADMIN_EMAILS` (comma-separated). Read once at task start and treated as the only
source of truth — an account removed from this list also loses the role in the database on
the next start, so a handover completes for somebody who never signs in again. Changing it
needs a service redeploy.

### `AfProvision`

`AF_PROVISION` — what happens to somebody who signs in with no tenant membership yet.
`invite` (the default here) = they land on a "you haven't been invited yet" page until an
admin adds them; `auto` = they are given a membership of the default tenant on the spot, so
`AF_OAUTH_ALLOWED_*` is the only thing between a stranger and a workspace. A tenant's own
`auto_join_domains` is checked BEFORE either.

★ Switching an existing stack to `invite` does NOT affect anyone already working: an
existing membership is consulted first, so this only stops NEW auto-admissions. (The CP's
built-in default is still `auto` — it is this template that starts closed, so a deployment
that never set it keeps its behaviour until you redeploy.)

### `AuthMode`

CP auth mode. `oauth` (default) = the CP-native login (Google and/or OIDC). `dev` = fixed
dev identity with NO login — sandbox/E2E gates only: restrict the ALB security group to your
own IP before using it (an internet-facing ALB with dev auth hands out an authenticated
session to anyone who reaches it).

## Alarms

### `CpAlarmEmail`

Where to send "the Control Plane cannot reach its database". Empty (the default) = no topic
and no alarm, and the only trace is the CP log.

⚠️ On 2026-09-01 that was the whole problem: the database went away, `/healthz` kept
answering 200, the ALB target stayed healthy, ECS stayed at steady state, and nobody found
out until a person tried to use the product. The metric is always collected; this parameter
is what makes it reach someone. Set it.
