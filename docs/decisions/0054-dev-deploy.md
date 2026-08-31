# 0054. The development deployment puts `develop` on with one command, and standing it up and tearing it down take the same shape (a golden's identity is its contents, not a reference)

English | [日本語](0054-dev-deploy.ja.md)

- Status: **adopted** (2026-08-23; P0/P1 implemented, not yet run for the first time on real
  hardware). The record of the investigation is [docs/73](../log/73-dev-deploy.md).
- See also: [0053-cp-arch-and-availability.md](0053-cp-arch-and-availability.md) (`CpArch` and dev images) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decisions 8 and 9 (`ImageTag` is shared by two images / reconciling the golden) /
  [docs/35](../log/35-packaging.md) (the real release path — which this does not go through)

## Context

The deployment we want for development is **our own**, stood up in a production-equivalent
configuration; the one with real users is a different deployment. ⚠️ **Which is which is not written
in this repository** (it is public, so neither deployment names nor FQDNs are left in code or
documents — the local environment file holds them). [docs/72](../log/72-cp-arch-and-availability.md)
§72.6 got "put the current code on without cutting a release" working on real hardware, but the
procedure was still four manual steps. A development deployment's value is in **how many times you do
it**, so having done it once and being able to repeat it are different things.

## Decision 1 — one entrance (`dev-deploy.sh`), and the exit is always `update.sh`

The pre-checks (the `CpArch`/manifest match, the empty-changeset check for a mutable tag, the golden's
staleness, the list of running workspaces) exist only in `update.sh`. Writing a second deploy path for
dev means **the checks are the first thing to drift** (the same shape as §72.6.1's decision that
"building goes through `release.sh`").

## Decision 2 — build with `dev-image.yml`, tagged `<next patch>-dev-<sha8>`

- Not a mutable tag (`:dev`). A mutable tag's only advantage — "the golden is not re-baked" — is
  obtained by decision 3, whereas it **makes the CP's version string unable to say which commit it
  is**. That is information that matters most on a development deployment.
- The tag's sha is taken from **the remote ref**, and after building it is reconciled against the
  run's `headSha`. CI builds origin's ref, so unpushed changes silently do not go in and the
  deployment "succeeds".

## Decision 3 — a golden's identity is held by contents (`af-image-fp`)

What `af-image` holds is **a reference string, not contents**. It breaks in both directions:

- **The same contents under a different tag** (which the development deployment does every time, since
  it builds only the CP and re-tags the workspace): a re-bake runs. Measured at about 10 minutes and
  two EC2 slots (§72.6.4).
- **Different contents under the same tag** (running a mutable tag): it still matches, so **only new
  members get a home baked from the old image**. That one is not a cost error but a correctness error.

Settled: when baking, stamp **`af-image-fp`** alongside `af-image`. Its value is **the same function**
as the restart badge (`imageFingerprint` = the set of per-platform manifest digests) folded into a
`sha256:` — not building the two sides with different functions is the rut `runtime_ecs_stale.go`
measured itself into.

⚠️ **When the fingerprint cannot be read, compare the strings** (not ECR / no permission / an old
golden with no fingerprint tag). Reading unknown as "mismatch" would **throw away every deployment's
golden the moment they upgrade**. There are four places that reconcile (Start's `goldenSnapshot`, the
baker's `goldenFor`, `rejectedAttempts` for the give-up count, and the Console's `PoolStatus`), and
**changing only one of them to compare contents makes the screen disagree with the actual behaviour**
(it would display the golden in use as "old"), so they change together.

## Decision 4 — whether to build the workspace is decided by the diff in `workspace/`

The ws image's build context is `workspace/` alone. With no diff, **re-tagging inside ECR** suffices;
building costs +593 seconds under QEMU (§72.5.1). ⚠️ Changes on the build-argument side (`BAKE_AGENT_CLIS`
and friends) do not show up in the diff, so in that case pass `--image both` explicitly.

## Decision 5 — never touch a deployment with real users. The marker lives in the local notes, not the repo

Moving `ImageTag` is an operation that raises a "restart required" badge for anyone working there.
`dev-deploy.sh` only touches a deployment whose local notes contain **`AF_DEV_DEPLOY=1`**.

★ The marker is not a default FQDN in the script, because **"which deployment is the development one"
is also part of a deployment's identity**. This repository is public, and deployment names, FQDNs and
coordinates are not left here.

## Decision 6 — point at a deployment by **AWS profile**. Keep only "the arguments needed to rebuild it"

Standing up, putting to sleep and tearing down are used **on every deployment**. The way to point at
one is `--profile/--region` — the shape `update.sh` and `release-ecr.sh` have had from the start.
**No layer where a person invents an alias is needed** — a profile already carries "which account,
with which permissions", and `~/.aws/config` is outside the repository.

- The concrete facts (stack names, the FQDN, whether there is a pool layer) are **read from the live
  deployment**. Names do not line up even when stood up from the same template (measured: one
  deployment's pool layer is `af-ecs-pool`, another's is `af-ecs-ec2-pool`), so rather than hard-coding
  a convention we read them **from reality**, from the export `<stack>-SlotLaunchTemplateId`.
- What is kept locally is **only the arguments needed to rebuild it** (`capture-env.sh`), stored in
  `~/.config/agent-fleet/deploy/<profile>.<region>/` — **outside the repository**. This repository is
  public, and the contents are account-specific (hosted zone ids, allowed emails, OAuth client ids).
- ⚠️ **Do not read the notes against a live deployment.** The notes go stale, so the moment reality is
  updated you would be running against "a deployment that is not the real one". The live values are
  always AWS's.
- ⚠️ **This is step zero of a teardown.** The templates are in the repo, but **what was passed to them
  exists only inside the deployment, and becomes unreadable the moment `delete-stack` is issued** (on
  2026-08-22 we got by with a hand-made JSON copy). `teardown.sh` refuses to run without the notes.
- No secrets are carried. An SSM SecureString is not a CFN argument so it does not enter, and
  `standup.sh` only checks **whether it exists**. ⚠️ But **some arguments look like secrets**
  (`BitbucketOauthKey`), so they are masked in the plan display and the dry run.

## Decision 7 — the default is "do nothing". What is protected is not against deletion but **the order**

- `teardown.sh` / `standup.sh` / `pause.sh --down` print the plan and exit unless `--yes` is given.
  With `--yes` and a terminal, they **make you type the FQDN** — with two deployments and real users on
  one of them, making a person confirm with their eyes which one they are aimed at is the only
  effective armour.
- Every breakage we actually measured was about order: tidying up before stopping the CP means **the
  running CP recreates it**; terminating a slot **leaves home's EBS behind**; deleting the stacks
  together makes **the deletion silently cancel** while an importer is present; and on the way up, the
  image goes in after 20, and unless 40's **new** launch template is passed to 30, **only the slots
  never start again**. So the gates are placed on the order
  (`deploy/local/ecs-lifecycle-stub-test.sh`).
- A teardown by default **leaves `/af-cp/*`** and leaves what `Persistence=retain` retained (the RDS
  final snapshot, EFS). Those are deleted only with an explicit `--purge-secrets` /
  `--purge-retained` — retain means somebody asked for it.

## Decision 8 — deploying directly from CI (GitHub OIDC) is not adopted

It would come down to one button, but it means **permanently** installing an OIDC provider and a role
with deploy permissions in the target account. Not worth it against the effort of one command. If it is
ever done, it should be **a manual dispatch, not automatic on every push** — an operation that moves
`ImageTag` should happen at a time a person chose.
